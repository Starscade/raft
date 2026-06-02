package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

//go:embed index.html
var content embed.FS

type QueryHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

type APIError struct {
	Error string `json:"error"`
}

func (h *QueryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		indexFile, err := fs.ReadFile(content, "index.html")
		if err != nil {
			h.sendError(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(indexFile)
		return
	}

	if r.Method != http.MethodPost {
		h.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Resilience: Limit request body size (1MB)
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.sendError(w, "Request body too large or unreadable", http.StatusBadRequest)
		return
	}

	// Robustness: Context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := h.db.QueryContext(ctx, string(body))
	if err != nil {
		h.sendError(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		h.sendError(w, "Failed to retrieve columns", http.StatusInternalServerError)
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
			h.sendError(w, "Failed to scan row", http.StatusInternalServerError)
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

	w.Header().Set("Content-Type", "application/json")
	if results == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	json.NewEncoder(w).Encode(results)
}

func (h *QueryHandler) sendError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(APIError{Error: msg})
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	port := os.Getenv("RAFT_PORT")
	if port == "" {
		port = "80"
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		logger.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	sql_file := os.Getenv("RAFT_INIT_SQL_FILE")
	if sql_file != "" {
		file_bytes, err := os.ReadFile(sql_file)
		if err != nil {
			logger.Error("failed to read SQL file", "err", err)
			os.Exit(1)
		}
		logger.Info("initializing", "sql", sql_file)
		if _, err := db.Exec(string(file_bytes)); err != nil {
			logger.Error("failed to initialize SQL", "err", err)
			os.Exit(1)
		}
	}

	handler := &QueryHandler{db: db, logger: logger}
	mux := http.NewServeMux()
	mux.Handle("/", handler)

	server := &http.Server{Addr: ":" + port, Handler: mux}

	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	logger.Info("server starting", "port", port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server failed", "err", err)
	}
}
