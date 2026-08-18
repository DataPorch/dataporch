//go:build integration

package mysql

import (
	"fmt"
	"testing"

	"github.com/adamraziv/dataporch/internal/execution"
)

func createMySQLDiscoveryFixture(t *testing.T, fixture *mysqlIntegrationFixture) {
	t.Helper()

	primary := testQuotedIdentifier(t, fixture.primaryDB)
	secondary := testQuotedIdentifier(t, fixture.secondaryDB)

	statements := []string{
		fmt.Sprintf(`CREATE TABLE %s.accounts (
            id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
            external_id VARCHAR(64) NOT NULL COMMENT 'external account id',
            balance DECIMAL(12,2) NOT NULL DEFAULT 0,
            state ENUM('active','disabled') NOT NULL,
            payload JSON NULL,
            binary_fixed BINARY(2) NULL,
            binary_var VARBINARY(8) NULL,
            binary_blob BLOB NULL,
            doubled_balance DECIMAL(12,2) GENERATED ALWAYS AS (balance * 2) STORED,
            PRIMARY KEY (id),
            UNIQUE KEY uq_accounts_external (external_id),
            CONSTRAINT chk_accounts_balance CHECK (balance >= 0)
        ) ENGINE=InnoDB`, primary),
		fmt.Sprintf(`CREATE TABLE %s.account_children (
            id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
            account_id BIGINT UNSIGNED NOT NULL,
            PRIMARY KEY (id),
            CONSTRAINT fk_account_children_account
                FOREIGN KEY (account_id) REFERENCES %s.accounts(id)
                ON DELETE CASCADE
        ) ENGINE=InnoDB`, primary, primary),
		fmt.Sprintf(`CREATE VIEW %s.active_accounts AS
            SELECT id, external_id FROM %s.accounts WHERE state = 'active'`, primary, primary),
		fmt.Sprintf(`CREATE TABLE %s.external_accounts (
            id BIGINT UNSIGNED NOT NULL,
            PRIMARY KEY (id)
        ) ENGINE=InnoDB`, secondary),
		fmt.Sprintf(`CREATE TABLE %s.external_children (
            id BIGINT UNSIGNED NOT NULL,
            external_id BIGINT UNSIGNED NOT NULL,
            PRIMARY KEY (id),
            CONSTRAINT fk_external_children_external
                FOREIGN KEY (external_id) REFERENCES %s.external_accounts(id)
        ) ENGINE=InnoDB`, primary, secondary),
		fmt.Sprintf("CREATE TABLE %s.`literal_%%_probe` (id INT PRIMARY KEY) ENGINE=InnoDB", primary),
	}
	for _, statement := range statements {
		testExecSQL(t, fixture.admin, statement)
	}
}

func constraintByKind(t *testing.T, constraints []execution.Constraint, kind string) execution.Constraint {
	t.Helper()

	for _, constraint := range constraints {
		if constraint.Kind == kind {
			return constraint
		}
	}

	t.Fatalf("constraint kind %q not found in %#v", kind, constraints)

	return execution.Constraint{}
}

func TestDiscoveryMySQLIntegration(t *testing.T) {
	t.Parallel()

	fixture := newMySQLIntegrationFixture(t)
	createMySQLDiscoveryFixture(t, fixture)

	primaryOpener := newMySQLIntegrationOpener(t, "mysql_primary", fixture.readerURI(t, fixture.primaryDB, fixture.password))

	primary, err := NewDiscoverer(primaryOpener)
	if err != nil {
		t.Fatalf("NewDiscoverer(primary) error = %v", err)
	}

	secondaryOpener := newMySQLIntegrationOpener(t, "mysql_secondary", fixture.readerURI(t, fixture.secondaryDB, fixture.password))

	secondary, err := NewDiscoverer(secondaryOpener)
	if err != nil {
		t.Fatalf("NewDiscoverer(secondary) error = %v", err)
	}

	primarySchemas, err := primary.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{SourceID: "mysql_primary", Limit: 10})
	if err != nil || len(primarySchemas.Schemas) != 1 || primarySchemas.Schemas[0].Name != fixture.primaryDB {
		t.Fatalf("primary schemas=%#v error=%v", primarySchemas, err)
	}

	secondarySchemas, err := secondary.ListSchemas(t.Context(), execution.SchemaDiscoveryRequest{SourceID: "mysql_secondary", Limit: 10})
	if err != nil || len(secondarySchemas.Schemas) != 1 || secondarySchemas.Schemas[0].Name != fixture.secondaryDB {
		t.Fatalf("secondary schemas=%#v error=%v", secondarySchemas, err)
	}

	tables, err := primary.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "mysql_primary", Schema: fixture.primaryDB, IncludeDescriptions: true, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}

	foundAccounts := false
	foundView := false

	for _, table := range tables.Tables {
		if table.Name == "accounts" && table.Kind == execution.RelationKindTable {
			foundAccounts = true
		}

		if table.Name == "active_accounts" && table.Kind == execution.RelationKindView {
			foundView = true
		}

		if table.Name == "external_accounts" {
			t.Fatal("primary discovery enumerated secondary database")
		}
	}

	if !foundAccounts || !foundView {
		t.Fatalf("tables=%#v, missing table/view", tables.Tables)
	}

	columns, err := primary.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "mysql_primary", Schema: fixture.primaryDB, Table: "accounts",
		IncludeDescriptions: true, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListColumns(accounts) error = %v", err)
	}

	for _, column := range columns.Columns {
		if column.Type.Affinity != "" {
			t.Fatalf("column %q affinity=%q, want empty", column.Name, column.Type.Affinity)
		}
	}

	primaryKey := constraintByKind(t, columns.Constraints, "primary_key")
	unique := constraintByKind(t, columns.Constraints, "unique")

	check := constraintByKind(t, columns.Constraints, "check")
	if !primaryKey.Validated || !unique.Validated || check.CheckExpression == nil || !check.Validated {
		t.Fatalf("constraints=%#v", columns.Constraints)
	}

	sameDB, err := primary.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "mysql_primary", Schema: fixture.primaryDB, Table: "account_children", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListColumns(account_children) error = %v", err)
	}

	if fk := constraintByKind(t, sameDB.Constraints, "foreign_key"); fk.Referenced == nil || fk.DeleteAction != "cascade" {
		t.Fatalf("same-db fk=%#v", fk)
	}

	crossDB, err := primary.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "mysql_primary", Schema: fixture.primaryDB, Table: "external_children", Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListColumns(external_children) error = %v", err)
	}

	if fk := constraintByKind(t, crossDB.Constraints, "foreign_key"); fk.Referenced != nil || len(fk.Columns) == 0 {
		t.Fatalf("cross-db fk=%#v", fk)
	}

	later, err := primary.ListColumns(t.Context(), execution.ColumnDiscoveryRequest{
		SourceID: "mysql_primary", Schema: fixture.primaryDB, Table: "accounts", Limit: 100, AfterOrdinal: 1,
	})
	if err != nil || len(later.Constraints) != 0 {
		t.Fatalf("later column page=%#v error=%v", later, err)
	}

	literal, err := primary.ListTables(t.Context(), execution.TableDiscoveryRequest{
		SourceID: "mysql_primary", Schema: fixture.primaryDB, Search: "%_", Limit: 100,
	})
	if err != nil {
		t.Fatalf("literal ListTables() error = %v", err)
	}

	foundLiteral := false

	for _, table := range literal.Tables {
		if table.Name == "literal_%_probe" {
			foundLiteral = true
		}
	}

	if !foundLiteral {
		t.Fatalf("literal search tables=%#v", literal.Tables)
	}

	for index := 1; index < len(tables.Tables); index++ {
		if tables.Tables[index-1].Name > tables.Tables[index].Name {
			t.Fatalf("table ordering is not binary deterministic: %#v", tables.Tables)
		}
	}
}
