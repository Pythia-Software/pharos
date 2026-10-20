//go:build ignore

// Built by eject-release.sh. Use the application's SQLite driver and functions:
// the system sqlite3 cannot validate indexes containing Query Table expressions.
package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"

	_ "github.com/gbdubs/pharos/internal/archive"
)

func checkCatalog(path string) error {
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	checked := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("integrity_check: %s", result)
		}
		checked = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !checked {
		return fmt.Errorf("integrity_check returned no result")
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: check-catalog CATALOG")
		os.Exit(2)
	}
	if err := checkCatalog(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
