package gorm

const createColumnExpressionsKey = "gorm:create_column_expressions"

// CreateWithColumnExpressions creates value while replacing selected database
// column values with SQL expressions. Keys are database column names.
func (s *DB) CreateWithColumnExpressions(value interface{}, expressions map[string]*SqlExpr) *DB {
	if len(expressions) == 0 {
		return s.Create(value)
	}
	return s.Set(createColumnExpressionsKey, expressions).Create(value)
}

func createColumnValue(scope *Scope, field *Field) interface{} {
	value := field.Field.Interface()
	rawExpressions, ok := scope.Get(createColumnExpressionsKey)
	if !ok {
		return value
	}
	expressions, ok := rawExpressions.(map[string]*SqlExpr)
	if !ok {
		return value
	}
	expression, ok := expressions[field.DBName]
	if !ok || expression == nil {
		return value
	}
	return expression
}
