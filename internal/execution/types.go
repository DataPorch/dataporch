package execution

import (
	"context"

	"github.com/adamraziv/dataporch/internal/access"
	"github.com/adamraziv/dataporch/internal/connection"
)

type Capability string

const CapabilityRelationalDatabase Capability = "relational_database"

type RelationKind string

const (
	RelationKindTable            RelationKind = "table"
	RelationKindPartitionedTable RelationKind = "partitioned_table"
	RelationKindView             RelationKind = "view"
	RelationKindMaterializedView RelationKind = "materialized_view"
	RelationKindForeignTable     RelationKind = "foreign_table"
)

type TypeCategory string

const (
	TypeCategoryBase       TypeCategory = "base"
	TypeCategoryArray      TypeCategory = "array"
	TypeCategoryDomain     TypeCategory = "domain"
	TypeCategoryEnum       TypeCategory = "enum"
	TypeCategoryComposite  TypeCategory = "composite"
	TypeCategoryRange      TypeCategory = "range"
	TypeCategoryMultirange TypeCategory = "multirange"
	TypeCategoryPseudo     TypeCategory = "pseudo"
	TypeCategoryOther      TypeCategory = "other"
)

type DataSource struct {
	ID           connection.ID   `json:"id"`
	Kind         connection.Kind `json:"kind"`
	Capabilities []Capability    `json:"capabilities"`
}

type Schema struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type Table struct {
	Name        string       `json:"name"`
	Kind        RelationKind `json:"kind"`
	Description *string      `json:"description,omitempty"`
}

type TypeReference struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type DataType struct {
	Schema            string         `json:"schema"`
	Name              string         `json:"name"`
	Category          TypeCategory   `json:"category"`
	Length            *int32         `json:"length"`
	Precision         *int32         `json:"precision"`
	Scale             *int32         `json:"scale"`
	TemporalPrecision *int32         `json:"temporal_precision"`
	IsArray           bool           `json:"is_array"`
	ElementType       *TypeReference `json:"element_type"`
	DomainBaseType    *TypeReference `json:"domain"`
}

type Identity struct {
	Generation string `json:"generation"`
}

type Generated struct {
	Kind       string `json:"kind"`
	Expression string `json:"expression"`
}

type Column struct {
	Name              string     `json:"name"`
	OrdinalPosition   int        `json:"ordinal_position"`
	FormattedType     string     `json:"formatted_type"`
	Type              DataType   `json:"type"`
	Nullable          bool       `json:"nullable"`
	DefaultExpression *string    `json:"default_expression"`
	Identity          *Identity  `json:"identity"`
	Generated         *Generated `json:"generated"`
	Description       *string    `json:"description,omitempty"`
}

type ConstraintReference struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

type Constraint struct {
	Name              string               `json:"name"`
	Kind              string               `json:"kind"`
	Columns           []string             `json:"columns"`
	Deferrable        bool                 `json:"deferrable"`
	InitiallyDeferred bool                 `json:"initially_deferred"`
	Referenced        *ConstraintReference `json:"referenced,omitempty"`
	MatchType         string               `json:"match_type,omitempty"`
	UpdateAction      string               `json:"update_action,omitempty"`
	DeleteAction      string               `json:"delete_action,omitempty"`
	NullsNotDistinct  *bool                `json:"nulls_not_distinct,omitempty"`
	CheckExpression   *string              `json:"check_expression,omitempty"`
	Validated         bool                 `json:"validated"`
}

type ListDataSourcesRequest struct {
	Search string `json:"search,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type ListDataSourcesResult struct {
	Sources    []DataSource `json:"sources"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type ListRelationalSchemasRequest struct {
	SourceID            connection.ID `json:"source_id"`
	Search              string        `json:"search,omitempty"`
	IncludeDescriptions bool          `json:"include_descriptions,omitempty"`
	Limit               *int          `json:"limit,omitempty"`
	Cursor              string        `json:"cursor,omitempty"`
}

type ListRelationalSchemasResult struct {
	SourceID   connection.ID `json:"source_id"`
	Schemas    []Schema      `json:"schemas"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type ListRelationalTablesRequest struct {
	SourceID            connection.ID `json:"source_id"`
	Schema              string        `json:"schema"`
	Search              string        `json:"search,omitempty"`
	IncludeDescriptions bool          `json:"include_descriptions,omitempty"`
	Limit               *int          `json:"limit,omitempty"`
	Cursor              string        `json:"cursor,omitempty"`
}

type ListRelationalTablesResult struct {
	SourceID   connection.ID `json:"source_id"`
	Schema     string        `json:"schema"`
	Tables     []Table       `json:"tables"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type ListRelationalColumnsRequest struct {
	SourceID            connection.ID `json:"source_id"`
	Schema              string        `json:"schema"`
	Table               string        `json:"table"`
	Search              string        `json:"search,omitempty"`
	IncludeDescriptions bool          `json:"include_descriptions,omitempty"`
	Limit               *int          `json:"limit,omitempty"`
	Cursor              string        `json:"cursor,omitempty"`
}

type ListRelationalColumnsResult struct {
	SourceID     connection.ID `json:"source_id"`
	Schema       string        `json:"schema"`
	Table        string        `json:"table"`
	RelationKind RelationKind  `json:"relation_kind"`
	Columns      []Column      `json:"columns"`
	Constraints  []Constraint  `json:"constraints"`
	NextCursor   string        `json:"next_cursor,omitempty"`
}

type SourceRegistry interface {
	List() []connection.Definition
	Lookup(connection.ID) (connection.Definition, error)
}

type Authorizer interface {
	Authorize(context.Context, access.Request) error
}

type RelationalDiscoverer interface {
	Kind() connection.Kind
	ListSchemas(context.Context, SchemaDiscoveryRequest) (SchemaDiscoveryPage, error)
	ListTables(context.Context, TableDiscoveryRequest) (TableDiscoveryPage, error)
	ListColumns(context.Context, ColumnDiscoveryRequest) (ColumnDiscoveryPage, error)
}

type Dependencies struct {
	Sources                  SourceRegistry
	Authorizer               Authorizer
	MaxLimit                 int
	RelationalDiscoverers    []RelationalDiscoverer
	RelationalQueryExecutors []RelationalQueryExecutor
}

type SchemaDiscoveryRequest struct {
	SourceID            connection.ID
	Search              string
	IncludeDescriptions bool
	Limit               int
	AfterName           string
}

type SchemaDiscoveryPage struct {
	Schemas []Schema
	HasMore bool
}

type TableDiscoveryRequest struct {
	SourceID            connection.ID
	Schema              string
	Search              string
	IncludeDescriptions bool
	Limit               int
	AfterName           string
}

type TableDiscoveryPage struct {
	Tables  []Table
	HasMore bool
}

type ColumnDiscoveryRequest struct {
	SourceID            connection.ID
	Schema              string
	Table               string
	Search              string
	IncludeDescriptions bool
	Limit               int
	AfterOrdinal        int
}

type ColumnDiscoveryPage struct {
	Columns      []Column
	RelationKind RelationKind
	Constraints  []Constraint
	HasMore      bool
}

var _ Authorizer = (*access.AllowAll)(nil)
