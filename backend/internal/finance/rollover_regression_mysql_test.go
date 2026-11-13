package finance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

func TestMySQLCurrentMonthRolloverAcrossEndpoints(t *testing.T) {
	db := testdb.Open(t, "TEST_MYSQL_REGRESSION_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	sg, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(sg)
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, sg)
	month, previous := first.Format("2006-01"), first.AddDate(0, -1, 0).Format("2006-01")
	service := finance.NewService(db, finance.WithClock(func() time.Time { return now }))
	identity := auth.NewService(db, nil, time.Hour)
	handler, err := auth.NewHTTPHandler(identity, auth.ModeLocal)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	handler.RegisterUserRoutes(router.Group("/api"), service)
	financeHTTP := finance.NewHTTPHandler(service, identity)
	financeHTTP.RegisterRoutes(router.Group("/api"))
	financeHTTP.RegisterDashboardRoutes(router.Group("/api"))
	fixture := &q1Environment{ctx: ctx, db: db, router: router, auth: identity, finance: service}

	for _, trigger := range []string{"me", "dashboard", "provider", "no_profile"} {
		t.Run(trigger, func(t *testing.T) {
			result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at)
				VALUES (?, 'integration-test-only', ?)`, "rollover_"+trigger, now.UTC())
			if err != nil {
				t.Fatal(err)
			}
			id, err := result.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			token, err := identity.IssueToken(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			user := &q1User{ID: id, Token: token}
			var prior *finance.Budget
			if trigger != "no_profile" {
				total := int64(10000)
				if _, err := service.SaveProfile(ctx, id, finance.ProfileInput{
					MonthlyDisposableCents: &total, Housing: "on_campus",
				}); err != nil {
					t.Fatal(err)
				}
				input := finance.BudgetInput{TotalCents: &total}
				warn := 9000
				input.WarnBP = &warn
				for i, category := range []insight.Category{
					insight.CategoryFood, insight.CategoryTransport, insight.CategoryHousingTuition,
					insight.CategoryDailyShopping, insight.CategoryStudyDigital, insight.CategoryEntertainmentOther,
				} {
					amount := int64((i + 1) * 100)
					input.Categories = append(input.Categories, finance.CategoryBudgetInput{
						Category: category, BudgetCents: &amount,
					})
				}
				prior, err = service.SaveBudget(ctx, id, previous, input)
				if err != nil {
					t.Fatal(err)
				}
			}
			checkMe := func() {
				t.Helper()
				var me auth.CurrentUserInfo
				q1JSON(t, fixture.request(t, "GET", "/api/me", nil, user, "bearer", false), 200, &me)
				if me.Onboarded != (trigger != "no_profile") || me.CurrentMonth != month {
					t.Fatalf("rollover /me: %+v", me)
				}
			}
			var dashboard insight.Snapshot
			checkDashboard := func() {
				t.Helper()
				q1JSON(t, fixture.request(t, "GET", user.path("/dashboard"), nil, user, "bearer", false), 200, &dashboard)
			}
			switch trigger {
			case "me", "no_profile":
				checkMe()
			case "dashboard":
				checkDashboard()
			case "provider":
				if _, err := service.Snapshot(ctx, id, ""); err != nil {
					t.Fatal(err)
				}
			}
			checkMe()
			checkDashboard()
			snapshot, err := service.Snapshot(ctx, id, month)
			if err != nil {
				t.Fatal(err)
			}
			dashboardJSON, _ := json.Marshal(dashboard)
			snapshotJSON, _ := json.Marshal(snapshot)
			if !bytes.Equal(dashboardJSON, snapshotJSON) {
				t.Fatal("dashboard differs from Provider with the same as_of")
			}
			if trigger == "no_profile" {
				if !slices.Contains(snapshot.Missing, "profile") || !slices.Contains(snapshot.Missing, "budget") {
					t.Fatalf("missing flags: %v", snapshot.Missing)
				}
				var count int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE user_id = ?`, id).Scan(&count); err != nil || count != 0 {
					t.Fatalf("no-profile request generated budget: %d %v", count, err)
				}
				return
			}
			var budget finance.Budget
			q1JSON(t, fixture.request(t, "GET", user.path("/budgets/")+month, nil, user, "bearer", false), 200, &budget)
			if budget.Generated || budget.Source != "copied_from_"+previous || budget.TotalCents != prior.TotalCents ||
				budget.WarnBP != prior.WarnBP || !reflect.DeepEqual(budget.Categories, prior.Categories) ||
				snapshot.TotalBudgetCents != budget.TotalCents || slices.Contains(snapshot.Missing, "budget") {
				t.Fatalf("budget/dashboard rollover mismatch: budget=%+v snapshot=%+v", budget, snapshot)
			}
			for i, category := range snapshot.Categories {
				if category.Category != budget.Categories[i].Category || category.BudgetCents != budget.Categories[i].BudgetCents {
					t.Fatalf("category numbers differ: %+v %+v", category, budget.Categories[i])
				}
			}
			// Hash excludes the hash field itself. Derive the audited object from
			// the real HTTP response so the test verifies the public representation.
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			hashField := bytes.LastIndex(raw, []byte(`,"hash":`))
			if hashField < 0 {
				t.Fatal("snapshot response is missing its audit hash")
			}
			digest := sha256.Sum256(append(raw[:hashField], '}'))
			if snapshot.Hash != fmt.Sprintf("sha256:%x", digest) {
				t.Fatalf("snapshot hash disagrees with budget-backed response: %s", fmt.Sprint(snapshot.Hash))
			}
		})
	}
}
