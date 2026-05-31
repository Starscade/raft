package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

func main() {
	port := os.Getenv("RAFT_PORT")
	if port == "" {
		port = "80"
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		panic(err)
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

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		printLog(r.RemoteAddr + " requested " + r.URL.Path)
		
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

	server := &http.Server{Addr: ":" + port}

	printLog("Listening on port " + port)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		printLog(err.Error())
	}

}

func printLog(text string) {
	fmt.Printf("\033[1m%s\033[0m %s\n", time.Now().Format(time.RFC3339), text)
}
