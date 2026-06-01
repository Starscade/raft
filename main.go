package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

//go:embed index.html
var content embed.FS

func main() {
	port := os.Getenv("RAFT_PORT")
	if port == "" {
		port = "80"
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sql_file := os.Getenv("RAFT_INIT_SQL_FILE")
	if sql_file != "" {
		file_bytes, err := os.ReadFile(sql_file)
		if err != nil {
			log.Fatal(err)
		}
		printLog("Initializing " + sql_file)
		if _, err := db.Exec(string(file_bytes)); err != nil {
			log.Fatal(err)
		}
	}
	index_file, err := fs.ReadFile(content, "index.html")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html")
			w.Write(index_file)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		rows, err := db.Query(string(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var results []map[string]interface{}
		for rows.Next() {
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}
			if err := rows.Scan(valuePtrs...); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			rowMap := make(map[string]interface{})
			for i, col := range columns {
				val := values[i]
				if b, ok := val.([]byte); ok {
					rowMap[col] = string(b)
				} else {
					rowMap[col] = val
				}
			}
			results = append(results, rowMap)
		}

		if results == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	})

	server := &http.Server{Addr: ":" + port, Handler: mux}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint

		printLog("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Fatalf("HTTP server Shutdown: %v", err)
		}
		close(idleConnsClosed)
	}()

	printLog("Listening on port " + port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("HTTP server ListenAndServe: %v", err)
	}

	<-idleConnsClosed
	printLog("Server stopped")
}

func printLog(text string) {
	fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), text)
}
