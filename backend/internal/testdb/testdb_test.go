package testdb

import "testing"

func TestDatabaseName(t *testing.T) {
	cases := []struct {
		name, dsn, want string
	}{
		{"url", "postgres://u:p@127.0.0.1:5432/smartpipe_test?sslmode=disable", "smartpipe_test"},
		{"production", "postgres://u:p@127.0.0.1:5432/smartpipe?sslmode=disable", "smartpipe"},
		{"keyword", "host=localhost dbname=smartpipe_test", "smartpipe_test"},
		{"url override rejects", "postgres://localhost/smartpipe_test?dbname=production", "production"},
		{"url override accepts", "postgres://localhost/production?dbname=smartpipe_test", "smartpipe_test"},
		{"duplicate keyword rejects", "host=localhost dbname=smartpipe_test dbname=production", "production"},
		{"duplicate keyword accepts", "host=localhost dbname=production dbname=smartpipe_test", "smartpipe_test"},
		{"quoted value", "host=localhost dbname='space name_test'", "space name_test"},
		{"invalid quote", "host=localhost dbname='smartpipe_test", ""},
		{"invalid url", "postgres://localhost/%zz", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := databaseName(tc.dsn); got != tc.want {
				t.Fatalf("databaseName = %q, want %q", got, tc.want)
			}
		})
	}
}
