// Package mysql implements the Driver interface.
package mysql

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"github.com/mattes/migrate/file"
	"github.com/mattes/migrate/migrate/direction"
	"regexp"
	"strconv"

	// Use fork instead of github.com/go-sql-driver/mysql, because it
	// has clientMultiStatements enabled.
	// see https://github.com/go-sql-driver/mysql/issues/66
	"github.com/mattes/mysql"
)

type Driver struct {
	db *sql.DB
}

const tableName = "schema_migrations"

func (driver *Driver) Initialize(url string) error {
	db, err := sql.Open("mysql", url)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		return err
	}
	driver.db = db

	if err := driver.ensureVersionTableExists(); err != nil {
		return err
	}
	return nil
}

func (driver *Driver) Close() error {
	if err := driver.db.Close(); err != nil {
		return err
	}
	return nil
}

func (driver *Driver) ensureVersionTableExists() error {
	if _, err := driver.db.Exec("CREATE TABLE IF NOT EXISTS " + tableName + " (version int not null primary key);"); err != nil {
		return err
	}
	return nil
}

func (driver *Driver) FilenameExtension() string {
	return "sql"
}

func (driver *Driver) Migrate(f file.File, pipe chan interface{}) {
	defer close(pipe)
	pipe <- f

	// http://go-database-sql.org/modifying.html, Working with Transactions
	// You should not mingle the use of transaction-related functions such as Begin() and Commit() with SQL statements such as BEGIN and COMMIT in your SQL code.
	tx, err := driver.db.Begin()
	if err != nil {
		pipe <- err
		return
	}

	if f.Direction == direction.Up {
		if _, err := tx.Exec("INSERT INTO "+tableName+" (version) VALUES (?)", f.Version); err != nil {
			pipe <- err
			if err := tx.Rollback(); err != nil {
				pipe <- err
			}
			return
		}
	} else if f.Direction == direction.Down {
		if _, err := tx.Exec("DELETE FROM "+tableName+" WHERE version = ?", f.Version); err != nil {
			pipe <- err
			if err := tx.Rollback(); err != nil {
				pipe <- err
			}
			return
		}
	}

	if err := f.ReadContent(); err != nil {
		pipe <- err
		return
	}

	if _, err := tx.Exec(string(f.Content)); err != nil {
		mysqlErr := err.(*mysql.MySQLError)

		re, err := regexp.Compile(`at line ([0-9]+)$`)
		if err != nil {
			pipe <- err
			if err := tx.Rollback(); err != nil {
				pipe <- err
			}
		}

		var lineNo int
		lineNoRe := re.FindStringSubmatch(mysqlErr.Message)
		if len(lineNoRe) == 2 {
			lineNo, err = strconv.Atoi(lineNoRe[1])
		}
		if err == nil {

			// get white-space offset
			wsLineOffset := 0
			b := bufio.NewReader(bytes.NewBuffer(f.Content))
			for {
				line, _, err := b.ReadLine()
				if err != nil {
					break
				}
				if bytes.TrimSpace(line) == nil {
					wsLineOffset += 1
				} else {
					break
				}
			}

			message := mysqlErr.Error()
			message = re.ReplaceAllString(message, fmt.Sprintf("at line %v", lineNo+wsLineOffset))

			errorPart := file.LinesBeforeAndAfter(f.Content, lineNo, 5, 5, true)
			pipe <- errors.New(fmt.Sprintf("%s\n\n%s", message, string(errorPart)))
		} else {
			pipe <- errors.New(mysqlErr.Error())
		}

		if err := tx.Rollback(); err != nil {
			pipe <- err
		}
		return
	}

	if err := tx.Commit(); err != nil {
		pipe <- err
		return
	}
}

func (driver *Driver) Version() (uint64, error) {
	var version uint64
	err := driver.db.QueryRow("SELECT version FROM " + tableName + " ORDER BY version DESC").Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		return 0, nil
	case err != nil:
		return 0, err
	default:
		return version, nil
	}
}
