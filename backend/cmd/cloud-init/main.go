// Command cloud-init creates the application's schema-scoped MySQL account.
// It runs once as an ECS task; the normal backend never receives the admin password.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
)

func main() {
	if err := initialise(); err != nil {
		// SQL errors may contain statement fragments; never print them or a DSN.
		log.Fatal("database bootstrap failed; check network, certificates and secret configuration")
	}
	log.Print("schema-scoped application account is ready")
}

func initialise() error {
	host, admin, app := os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_ADMIN_PASSWORD"), os.Getenv("MYSQL_UNICHATGO_PASSWD")
	if host == "" || admin == "" || !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).MatchString(app) {
		return errors.New("missing or invalid bootstrap configuration")
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net = "dbadmin", admin, "tcp"
	cfg.Addr, cfg.DBName, cfg.TLSConfig = net.JoinHostPort(host, "3306"), "unichatgo", "true"
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 10*time.Second, 15*time.Second, 15*time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Secrets Manager generates the password with ExcludePunctuation=true.
	// Validate above before inserting it into account-management SQL; ordinary
	// DML remains parameterised elsewhere in the application.
	for _, statement := range []string{
		fmt.Sprintf("CREATE USER IF NOT EXISTS 'unichatgo'@'%%' IDENTIFIED BY '%s' REQUIRE SSL", app),
		fmt.Sprintf("ALTER USER 'unichatgo'@'%%' IDENTIFIED BY '%s' REQUIRE SSL", app),
		"GRANT ALL PRIVILEGES ON unichatgo.* TO 'unichatgo'@'%'",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
