// Command seed creates local demo and load-test accounts in a MySQL database.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"
	_ "time/tzdata"

	"unichatgo/internal/auth"
	"unichatgo/internal/config"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/storage"
	"unichatgo/migrations"
)

type options struct {
	configPath string
	count      int
	prefix     string
	month      string
	output     string
	demo       bool
}

var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,20}$`)

func main() {
	var opts options
	flag.StringVar(&opts.configPath, "config", os.Getenv("UNICHATGO_CONFIG"), "Local JSON configuration path")
	flag.IntVar(&opts.count, "count", 1, "Number of new local accounts")
	flag.StringVar(&opts.prefix, "prefix", "demo", "Username prefix; use a new prefix for each run")
	flag.StringVar(&opts.month, "month", "", "Budget/demo month, YYYY-MM; defaults to the current Singapore month")
	flag.StringVar(&opts.output, "output", "", "New private CSV path for generated credentials (required)")
	flag.BoolVar(&opts.demo, "demo", true, "Populate the first account with deterministic sample expenses")
	flag.Parse()
	if err := run(context.Background(), opts, time.Now(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Seed failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options, now time.Time, output io.Writer) error {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		return err
	}
	start, days, err := seedMonth(opts.month, now.In(location))
	if err != nil {
		return err
	}
	opts.month = start.Format("2006-01")
	if opts.count < 1 || opts.count > 100000 || !prefixPattern.MatchString(opts.prefix) {
		return errors.New("count must be 1–100000 and prefix must be 1–20 letters, digits or underscores")
	}
	if opts.output == "" {
		return errors.New("a new credentials CSV path is required with -output")
	}
	if mode := os.Getenv("AUTH_MODE"); mode != "" && mode != auth.ModeLocal {
		return errors.New("seed requires AUTH_MODE=local")
	}
	if driver := os.Getenv("UNICHATGO_DB"); driver != "" && driver != "mysql" {
		return errors.New("seed requires MySQL")
	}
	// Never overwrite an existing credentials file or print generated secrets.
	file, err := os.OpenFile(opts.output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("cannot create credentials file; choose a new writable path")
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	if err := writer.Write([]string{"user_id", "username", "password"}); err != nil {
		return err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return errors.New("cannot load local JSON configuration")
	}
	if cfg.Async.Enabled {
		return errors.New("seed requires async.enabled=false")
	}
	db, err := storage.Open("mysql", cfg)
	if err != nil {
		return errors.New("cannot connect to MySQL")
	}
	defer db.Close()
	var asyncTables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema=DATABASE() AND table_name='execution_control'`).Scan(&asyncTables); err != nil {
		return errors.New("cannot inspect database schema")
	}
	if asyncTables > 0 {
		return errors.New("refusing to seed a database with async execution installed")
	}
	if err := storage.Migrate(db, "mysql"); err != nil {
		return errors.New("cannot initialize legacy schema")
	}
	migrationCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err = migrations.Apply(migrationCtx, db)
	cancel()
	if err != nil {
		return errors.New("cannot apply UniFinSG migrations")
	}
	// Check the entire requested set before inserting any accounts. Concurrent
	// seed runs remain protected by the users.username unique constraint.
	for i := 1; i <= opts.count; i++ {
		var exists bool
		username := seedUsername(opts.prefix, i)
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username=?)`, username).Scan(&exists); err != nil {
			return errors.New("cannot check existing seed accounts")
		}
		if exists {
			return fmt.Errorf("account %s already exists; choose a new prefix", username)
		}
	}
	accounts := auth.NewService(db, nil, time.Hour)
	finances := finance.NewService(db, finance.WithClock(func() time.Time { return now }))
	for i := 1; i <= opts.count; i++ {
		password, err := randomPassword()
		if err != nil {
			return errors.New("cannot generate account credentials")
		}
		username := seedUsername(opts.prefix, i)
		user, err := accounts.RegisterLocalUser(ctx, username, password)
		if err != nil {
			return fmt.Errorf("cannot create account %s; previous accounts are recorded in the CSV", username)
		}
		// Persist each new account immediately, including when a later finance
		// operation fails. A partial run never loses already created credentials.
		if err := writer.Write([]string{strconv.FormatInt(user.ID, 10), username, password}); err != nil {
			return fmt.Errorf("cannot save credentials for %s", username)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return fmt.Errorf("cannot flush credentials for %s", username)
		}
		if err := file.Sync(); err != nil {
			return fmt.Errorf("cannot persist credentials for %s", username)
		}
		disposable, budget := int64(120000), int64(100000)
		if _, err := finances.SaveProfile(ctx, user.ID, finance.ProfileInput{
			MonthlyDisposableCents: &disposable, Housing: "on_campus", DefaultBudgetCents: &budget,
		}); err != nil {
			return fmt.Errorf("cannot save profile for %s; credentials remain in the CSV", username)
		}
		if _, err := finances.GetOrCreateBudget(ctx, user.ID, opts.month); err != nil {
			return fmt.Errorf("cannot generate budget for %s; credentials remain in the CSV", username)
		}
		if opts.demo && i == 1 {
			for _, transaction := range demoTransactions(start, days) {
				if _, err := finances.CreateTransaction(ctx, user.ID, transaction); err != nil {
					return fmt.Errorf("cannot finish demo expenses for %s; credentials remain in the CSV", username)
				}
			}
		}
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close credentials file")
	}
	_, err = fmt.Fprintf(output, "Created %d local accounts for %s. Credentials: %s\n", opts.count, opts.month, opts.output)
	return err
}

func seedUsername(prefix string, index int) string {
	return fmt.Sprintf("%s_%04d", prefix, index)
}

func randomPassword() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func seedMonth(month string, now time.Time) (time.Time, int, error) {
	if month == "" {
		month = now.Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, now.Location())
	if err != nil || start.Year() < 1 || start.Format("2006-01") != month {
		return time.Time{}, 0, errors.New("month must be a valid calendar month in YYYY-MM")
	}
	current, _ := time.ParseInLocation("2006-01", now.Format("2006-01"), now.Location())
	if start.After(current) {
		return time.Time{}, 0, errors.New("demo month cannot be in the future")
	}
	days := start.AddDate(0, 1, -1).Day()
	if start.Equal(current) {
		days = now.Day()
	}
	return start, days, nil
}

func demoTransactions(start time.Time, days int) []finance.TransactionInput {
	note := "Fictional demo expense"
	var transactions []finance.TransactionInput
	add := func(day int, amount int64, category insight.Category) {
		transactions = append(transactions, finance.TransactionInput{
			AmountCents: amount, SpentOn: start.AddDate(0, 0, day-1).Format("2006-01-02"),
			Category: category, Note: &note,
		})
	}
	for day := 1; day <= days; day++ {
		add(day, 800+int64(day%5)*100, insight.CategoryFood)
		date := start.AddDate(0, 0, day-1)
		if date.Weekday() != time.Saturday && date.Weekday() != time.Sunday {
			add(day, 250, insight.CategoryTransport)
		}
		for _, item := range []struct {
			day      int
			amount   int64
			category insight.Category
		}{
			{1, 25000, insight.CategoryHousingTuition},
			{5, 12000, insight.CategoryDailyShopping},
			{10, 9000, insight.CategoryStudyDigital},
			{15, 8000, insight.CategoryEntertainmentOther},
			{20, 13000, insight.CategoryFood},
		} {
			if day == item.day {
				add(day, item.amount, item.category)
			}
		}
	}
	return transactions
}
