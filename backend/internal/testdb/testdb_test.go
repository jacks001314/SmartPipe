package testdb

import "testing"

func TestDatabaseName(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@127.0.0.1:5432/smartpipe_test?sslmode=disable": "smartpipe_test",
		"postgres://u:p@127.0.0.1:5432/smartpipe?sslmode=disable":      "smartpipe",
		"host=localhost dbname=smartpipe_test":                        "smartpipe_test",
	}
	for dsn, want := range cases {
		if got := databaseName(dsn); got != want {
			t.Fatalf("databaseName(%q) = %q, want %q", dsn, got, want)
		}
	}
}