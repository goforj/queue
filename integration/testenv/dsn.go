package testenv

import "fmt"

func MySQLDSN(addr string) string {
	return fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", addr)
}

func PostgresDSN(addr string) string {
	return fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", addr)
}
