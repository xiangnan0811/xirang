// Command recover-admin performs the explicit break-glass administrator
// recovery procedure. It does not run migrations or seed/bootstrap users.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
)

func main() {
	username := flag.String("username", "", "existing account username to promote")
	reason := flag.String("reason", "", "bounded operator reason (8-512 characters)")
	confirmation := flag.String("confirmation", "", "one-time confirmation value matching XIRANG_BREAK_GLASS_CONFIRMATION")
	flag.Parse()
	if *username == "" || *reason == "" || *confirmation == "" {
		fmt.Fprintln(os.Stderr, "usage: recover-admin -username <existing-user> -reason <reason> -confirmation <value>")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	db, err := database.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		defer func() { _ = sqlDB.Close() }()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := bootstrap.BreakGlassPromoteAdmin(ctx, db, bootstrap.BreakGlassRequest{
		Username:     *username,
		Reason:       *reason,
		Confirmation: *confirmation,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "break-glass recovery failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("break-glass recovery completed")
}
