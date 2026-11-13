package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"unichatgo/internal/agent"
	"unichatgo/internal/agentv2"
	"unichatgo/internal/api"
	"unichatgo/internal/auth"
	"unichatgo/internal/blob"
	"unichatgo/internal/config"
	"unichatgo/internal/credits"
	"unichatgo/internal/crypto"
	"unichatgo/internal/drafts"
	"unichatgo/internal/eval"
	"unichatgo/internal/execution"
	"unichatgo/internal/finance"
	"unichatgo/internal/llm"
	"unichatgo/internal/llmconfig"
	"unichatgo/internal/redis"
	"unichatgo/internal/service/assistant"
	"unichatgo/internal/storage"
	"unichatgo/internal/tools"
	"unichatgo/internal/worker"
	"unichatgo/migrations"

	"github.com/gin-gonic/gin"
)

func main() {
	cfgPath := os.Getenv("UNICHATGO_CONFIG")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := config.ApplyRuntimeEnvironment(cfg); err != nil {
		log.Fatalf("configure environment: %v", err)
	}
	if cfg.Async.Enabled {
		log.Fatal("UniFinSG does not support the inherited async framework; set async.enabled=false")
	}
	evalClientIP, err := eval.NewTrustedProxyIPResolver(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		log.Fatalf("configure trusted proxies: %v", err)
	}

	dbType := os.Getenv("UNICHATGO_DB")
	if dbType == "" {
		dbType = "sqlite3"
	}
	log.Printf("dbType: %s\n", dbType)
	db, err := storage.Open(dbType, cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if cfg.Async.Enabled && dbType != "mysql" {
		log.Fatal("async execution requires MySQL")
	}
	if !cfg.Async.Enabled && dbType == "mysql" {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='execution_control'`).Scan(&exists); err != nil {
			log.Fatal(err)
		}
		if exists > 0 {
			log.Fatal("async migration installed: refusing legacy writer; use a compatible async version for rollback")
		}
	}
	rdb, err := redis.NewRedisClient(cfg)
	if err != nil {
		log.Fatalf("create redis client: %v", err)
	}
	defer rdb.Close()

	// Create necessary tables: users, apiKeys, sessions, messages
	if err := storage.Migrate(db, dbType); err != nil {
		log.Fatalf("migrate database: %v", err)
	}
	if dbType == "mysql" && !cfg.Async.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := migrations.Apply(ctx, db)
		cancel()
		if err != nil {
			log.Fatalf("migrate finance database: %v", err)
		}
	}
	if cfg.Async.Enabled {
		if err := execution.Migrate(context.Background(), db); err != nil {
			log.Fatalf("async migrate: %v", err)
		}
	}

	assistantService, err := assistant.NewService(db)
	if err != nil {
		log.Fatalf("init assistant service: %v", err)
	}
	workerCfg := worker.DispatcherConfig{
		MinWorkers:        cfg.BasicConfig.MinWorkers,
		MaxWorkers:        cfg.BasicConfig.MaxWorkers,
		QueueSize:         cfg.BasicConfig.QueueSize,
		WorkerIdleTimeout: time.Duration(cfg.BasicConfig.WorkerIdleTimeout) * time.Minute,
	}
	cleanCtx, cleanCancel := context.WithCancel(context.Background())
	defer cleanCancel()
	cleanInterval := time.Duration(cfg.BasicConfig.TempCleanInterval) * time.Minute
	if cleanInterval <= 0 {
		cleanInterval = assistant.DefaultTempFileCleanupInterval
	}
	if !cfg.Async.Enabled {
		assistantService.StartTempFileCleaner(cleanCtx, cleanInterval)
	}
	authService := auth.NewService(db, rdb, 24*time.Hour)
	fileBase := cfg.BasicConfig.FileBaseDir
	if fileBase == "" {
		fileBase = "./data/uploads"
	}
	tempTTL := time.Duration(cfg.BasicConfig.TempFileTTL) * time.Minute
	if tempTTL <= 0 {
		tempTTL = assistant.DefaultTempFileTTL
	}
	var handlers *api.Handler
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var runtime *execution.Runtime
	workerOnly := false
	done := make(chan struct{})
	if cfg.Async.Enabled {
		role := cfg.Async.Role
		if role == "" {
			role = "all"
		}
		if role != "api" && role != "worker" && role != "all" {
			log.Fatal("invalid async role")
		}
		timeout := time.Duration(cfg.Async.TaskTimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		repo := execution.NewRepository(db, execution.Limits{SessionQueue: cfg.Async.SessionQueue, UserQueue: cfg.Async.UserQueue, AcceptRPM: cfg.Async.AcceptRPM, TaskTimeout: timeout})
		if err := repo.CheckSchema(rootCtx); err != nil {
			log.Fatalf("async schema/write latch: %v", err)
		}
		if err := repo.BindAdmission(rootCtx); err != nil {
			log.Fatalf("admission configuration: %v", err)
		}
		handlers = api.NewAsyncHandler(assistantService, authService, repo, cfg)
		quota := cfg.Async.WithQuotaDefaults()
		limit := execution.QuotaLimit{Concurrency: quota.Concurrency, RPM: quota.RPM, TPM: quota.TPM}
		runtime = &execution.Runtime{Repo: repo, Redis: rdb.Raw(), Provider: execution.NewHTTPProvider(timeout), Decrypt: assistantService.OpenExecution, Workers: cfg.Async.Workers, Quota: execution.QuotaPolicy{Platform: limit, Credential: limit, User: execution.QuotaLimit{Concurrency: quota.UserConcurrency, RPM: quota.RPM, TPM: quota.TPM}, Grace: time.Minute}}
		if err := repo.BindPolicy(rootCtx, runtime.Quota); err != nil {
			log.Fatalf("quota configuration: %v", err)
		}
		fingerprint, err := assistant.ExecutionKeyFingerprint()
		if err != nil {
			log.Fatal("invalid execution encryption key")
		}
		if err = repo.BindEncryptionKey(rootCtx, fingerprint); err != nil {
			log.Fatalf("execution encryption key: %v", err)
		}
		type routingConfig struct {
			BaseURL    string `json:"base_url"`
			QuotaGroup string `json:"quota_group"`
		}
		routing := make(map[string]routingConfig, len(cfg.Providers))
		for name, p := range cfg.Providers {
			routing[name] = routingConfig{BaseURL: p.BaseURL, QuotaGroup: p.QuotaGroup}
		}
		routingJSON, err := json.Marshal(routing)
		if err != nil {
			log.Fatal("invalid provider routing configuration")
		}
		routingHash := sha256.Sum256(routingJSON)
		if err = repo.BindProviderRouting(rootCtx, hex.EncodeToString(routingHash[:])); err != nil {
			log.Fatalf("provider routing: %v", err)
		}
		if role != "api" {
			go func() { defer close(done); runtime.Run(rootCtx) }()
		} else {
			close(done)
		}
		if role == "worker" {
			workerOnly = true
		}
	} else {
		handlers = api.NewHandler(assistantService, authService, workerCfg, fileBase, tempTTL, rdb)
		close(done)
	}

	router := gin.New()
	// The callback query contains a single-use authorization code and state.
	// Keep the usual access logger for other endpoints without recording it.
	router.Use(gin.LoggerWithConfig(gin.LoggerConfig{SkipPaths: []string{"/api/auth/callback"}}), gin.Recovery())
	if !workerOnly {
		if cfg.Async.Enabled {
			handlers.RegisterV2Routes(router)
		} else {
			applicationAuth, err := auth.NewHTTPHandler(authService, os.Getenv("AUTH_MODE"))
			if err != nil {
				log.Fatalf("configure authentication: %v", err)
			}
			var cognito *auth.CognitoHandler
			var deleter auth.CognitoUserDeleter
			if os.Getenv("AUTH_MODE") == auth.ModeCognito {
				if dbType != "mysql" {
					log.Fatal("Cognito authentication requires MySQL")
				}
				cognitoConfig := auth.CognitoConfigFromEnv()
				cognito, err = auth.NewCognitoHandler(authService, cognitoConfig)
				if err != nil {
					log.Fatalf("configure Cognito: %v", err)
				}
				ctx, cancel := context.WithTimeout(rootCtx, 10*time.Second)
				deleter, err = auth.NewAWSCognitoDeleter(ctx, cognitoConfig.Region, cognitoConfig.UserPoolID)
				cancel()
				if err != nil {
					log.Print("warning: Cognito account deletion is unavailable; local deletion remains enabled")
				}
			}
			if dbType == "mysql" {
				account, err := auth.NewAccountHTTPHandler(authService, os.Getenv("AUTH_MODE"), deleter)
				if err != nil {
					log.Fatalf("configure account lifecycle: %v", err)
				}
				applicationAuth.SetAccountHandler(account)
			}
			// The inherited v1 chat routes are offline from 2.0 onward. Their
			// handlers remain for the legacy regression suite only.
			handlers.RegisterV2Routes(router, applicationAuth)
			if cognito != nil {
				cognito.RegisterRoutes(router.Group("/api"))
			}
			if dbType == "mysql" {
				financeService := finance.NewService(db)
				applicationAuth.RegisterUserRoutes(router.Group("/api"), financeService)
				financeHTTP := finance.NewHTTPHandler(financeService, authService)
				financeHTTP.RegisterRoutes(router.Group("/api"))
				financeHTTP.RegisterTransactionRoutes(router.Group("/api"))
				financeHTTP.RegisterDashboardRoutes(router.Group("/api"))
				agentHandler, err := agent.NewHandler(db, rdb, financeService, nil)
				if err != nil {
					log.Fatalf("configure agent: %v", err)
				}
				agentHandler.RegisterRoutes(router.Group("/api"), authService)

				// P1: BYOK configuration and daily platform credits. These
				// routes exist only for MySQL in the non-async deployment;
				// SQLite keeps the v1 surface.
				llmCipher, err := crypto.NewCipherFromEnv()
				if err != nil {
					log.Fatalf("configure llm key encryption: %v", err)
				}
				allowInsecureBYOK := llm.AllowInsecureFromEnv()
				llmConfigStore := llmconfig.NewMySQLStore(db)
				llmConfigService := llmconfig.NewService(llmConfigStore, llmCipher, llmconfig.Options{
					AllowInsecure: allowInsecureBYOK,
					Limiter:       llmconfig.NewLimiter(rdb.Raw()),
				})
				llmconfig.NewHTTPHandler(llmConfigService, authService).RegisterRoutes(router.Group("/api"))
				llmResolver := llm.NewResolver(llmConfigStore, llmCipher, llm.PlatformFromEnv(), allowInsecureBYOK)
				creditsService := credits.NewService(credits.NewRedisStore(rdb.Raw()), credits.ConfigFromEnv())
				credits.NewHTTPHandler(creditsService, llmResolver, authService).RegisterRoutes(router.Group("/api"))

				// P2/P3: conversation API, tools and the SSE turn runner.
				draftService := drafts.NewService(
					drafts.NewMySQLStore(db), financeService, agentDraftTTL(),
				)
				toolRegistry, err := tools.NewAgentRegistry(financeService, draftService, nil)
				if err != nil {
					log.Fatalf("configure agent tools: %v", err)
				}
				if webSearch, enabled := tools.WebSearchFromEnv(); enabled {
					if err := tools.RegisterWebSearch(toolRegistry, webSearch); err != nil {
						log.Printf("warning: web_search disabled: %v", err)
					}
				}
				maxSteps := 0
				if raw := os.Getenv("AGENT_MAX_STEPS"); raw != "" {
					value, err := strconv.Atoi(raw)
					if err != nil || value < 1 || value > 20 {
						log.Fatalf("invalid AGENT_MAX_STEPS: %q", raw)
					}
					maxSteps = value
				}
				// Private image storage: local disk in development, a private
				// S3 bucket in production. An invalid driver fails startup.
				uploadStore, err := blob.FromEnv(rootCtx)
				if err != nil {
					log.Fatalf("configure upload storage: %v", err)
				}
				uploadTTL := uploadTTLFromEnv()
				conversationHandler := agentv2.NewHandler(
					agentv2.NewMySQLStore(db),
					agentv2.NewRunner(toolRegistry, maxSteps, 90*time.Second).
						WithDrafts(draftService).
						WithDuplicates(agentv2.NewMySQLDuplicateFinder(db)),
					llmResolver,
					creditsService,
					draftService,
					agentv2.NewRedisCoordination(rdb.Raw()),
					agentv2.NewRateLimiter(rdb.Raw()),
					authService,
					agentv2.WithUploads(agentv2.NewMySQLUploadStore(db), uploadStore, uploadTTL),
				)
				conversationHandler.RegisterRoutes(router.Group("/api"))
				conversationHandler.StartUploadCleaner(rootCtx, time.Hour)

				// Expired drafts are also lazily expired on reads; this sweep
				// keeps abandoned sessions and their turns consistent.
				go expireDraftsPeriodically(rootCtx, draftService)

				eval.NewHTTPHandler(eval.NewService(db, rdb),
					eval.WithTrustedClientIPResolver(evalClientIP)).RegisterRoutes(router.Group("/api"))
			} else {
				log.Print("warning: BYOK, credits and v2 agent routes require MySQL; they are not registered")
			}
		}
	}
	registerHealthRoutes(router, db.PingContext, func(ctx context.Context) error {
		return rdb.Raw().Ping(ctx).Err()
	})
	if runtime != nil {
		router.GET("/internal/execution/metrics", func(c *gin.Context) {
			secret := os.Getenv("UNICHATGO_OPERATIONS_TOKEN")
			if secret == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+secret)) != 1 {
				c.AbortWithStatus(404)
				return
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
			defer cancel()
			metrics, err := runtime.Metrics(ctx)
			if err != nil {
				c.JSON(503, gin.H{"error": "metrics_unavailable"})
				return
			}
			c.JSON(200, metrics)
		})
	}

	addr := cfg.BasicConfig.ServerAddress
	if addr == "" {
		addr = ":8090"
	}

	server := &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-rootCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server stopped: %v", err)
		stop()
	}
	<-done
}

// agentDraftTTL reads AGENT_DRAFT_TTL_MINUTES (default 30).
func agentDraftTTL() time.Duration {
	raw := os.Getenv("AGENT_DRAFT_TTL_MINUTES")
	if raw == "" {
		return drafts.DefaultTTL
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 24*60 {
		log.Fatalf("invalid AGENT_DRAFT_TTL_MINUTES: %q", raw)
	}
	return time.Duration(value) * time.Minute
}

// uploadTTLFromEnv reads UPLOAD_TTL_HOURS (default 24).
func uploadTTLFromEnv() time.Duration {
	raw := os.Getenv("UPLOAD_TTL_HOURS")
	if raw == "" {
		return 24 * time.Hour
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 24*30 {
		log.Fatalf("invalid UPLOAD_TTL_HOURS: %q", raw)
	}
	return time.Duration(value) * time.Hour
}

// expireDraftsPeriodically sweeps overdue drafts once an hour. Reads also
// expire lazily, so this only closes abandoned turns.
func expireDraftsPeriodically(ctx context.Context, service *drafts.Service) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expireCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := service.ExpirePending(expireCtx, 0)
			cancel()
			if err != nil {
				log.Printf("expire agent drafts: %v", err)
			}
		}
	}
}
