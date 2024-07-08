package trigger

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/quix-labs/flash/pkg/types"
	"strings"
)

func (d *Driver) getCreateTriggerSqlForEvent(listenerUid string, l *types.ListenerConfig, e *types.Operation) (string, string, error) {
	uniqueName, err := d.getUniqueIdentifierForListenerEvent(listenerUid, e)
	if err != nil {
		return "", "", err
	}

	operation, err := d.getOperationNameForEvent(e)
	if err != nil {
		return "", "", err
	}

	triggerName := uniqueName + "_trigger"
	triggerFnName := uniqueName + "_fn"
	eventName := uniqueName + "_event"

	var statement string
	if len(l.Fields) == 0 {
		statement = fmt.Sprintf(`
			CREATE OR REPLACE FUNCTION "%s"."%s"() RETURNS trigger AS $trigger$
			BEGIN 
				PERFORM pg_notify('%s', JSONB_BUILD_OBJECT('old',to_jsonb(OLD),'new',to_jsonb(NEW))::TEXT);
				RETURN COALESCE(NEW, OLD);
			END;
			$trigger$ LANGUAGE plpgsql VOLATILE;`,
			d.Config.Schema, triggerFnName, eventName)
	} else {
		var rawFields, rawConditionSql string

		switch operation {
		case "TRUNCATE":
			rawFields = "null"
		case "DELETE":
			jsonFields := make([]string, len(l.Fields))
			for i, field := range l.Fields {
				jsonFields[i] = fmt.Sprintf(`'%s', OLD."%s"`, field, field)
			}
			rawFields = fmt.Sprintf(`JSONB_BUILD_OBJECT('old',JSONB_BUILD_OBJECT(%s))::TEXT`, strings.Join(jsonFields, ","))
		case "INSERT":
			jsonFields := make([]string, len(l.Fields))
			for i, field := range l.Fields {
				jsonFields[i] = fmt.Sprintf(`'%s', NEW."%s"`, field, field)
			}
			rawFields = fmt.Sprintf(`JSONB_BUILD_OBJECT('new',JSONB_BUILD_OBJECT(%s))::TEXT`, strings.Join(jsonFields, ","))
		case "UPDATE":
			oldJsonFields := make([]string, len(l.Fields))
			for i, field := range l.Fields {
				oldJsonFields[i] = fmt.Sprintf(`'%s', OLD."%s"`, field, field) //TODO DISTINCTION OLD NEW
			}
			newJsonFields := make([]string, len(l.Fields))
			for i, field := range l.Fields {
				newJsonFields[i] = fmt.Sprintf(`'%s', NEW."%s"`, field, field) //TODO DISTINCTION OLD NEW
			}
			rawFields = fmt.Sprintf(`JSONB_BUILD_OBJECT('old',JSONB_BUILD_OBJECT(%s),'new',JSONB_BUILD_OBJECT(%s))::TEXT`, strings.Join(oldJsonFields, ","), strings.Join(newJsonFields, ","))

			// Add condition to trigger only if fields where updated
			rawConditions := make([]string, len(l.Fields))
			for i, field := range l.Fields {
				rawConditions[i] = fmt.Sprintf(`(OLD."%s" IS DISTINCT FROM NEW."%s")`, field, field)
			}
			rawConditionSql = strings.Join(rawConditions, " OR ")
		}

		if rawConditionSql == "" {
			statement = fmt.Sprintf(`
				CREATE OR REPLACE FUNCTION "%s"."%s"() RETURNS trigger AS $trigger$
				BEGIN 
					PERFORM pg_notify('%s', %s);
					RETURN COALESCE(NEW, OLD);
				END;
				$trigger$ LANGUAGE plpgsql VOLATILE;`,
				d.Config.Schema, triggerFnName, eventName, rawFields)
		} else {
			statement = fmt.Sprintf(`
				CREATE OR REPLACE FUNCTION "%s"."%s"() RETURNS trigger AS $trigger$
				BEGIN
					IF %s THEN
						PERFORM pg_notify('%s', %s);
					END IF;
					RETURN COALESCE(NEW, OLD);
				END;
				$trigger$ LANGUAGE plpgsql VOLATILE;`,
				d.Config.Schema, triggerFnName, rawConditionSql, eventName, rawFields)
		}
	}

	if operation != "TRUNCATE" {
		statement += fmt.Sprintf(`
			CREATE OR REPLACE TRIGGER "%s" AFTER %s ON %s FOR EACH ROW EXECUTE PROCEDURE "%s"."%s"();`,
			triggerName, operation, d.sanitizeTableName(l.Table), d.Config.Schema, triggerFnName)
	} else {
		statement += fmt.Sprintf(`
			CREATE OR REPLACE TRIGGER "%s" BEFORE TRUNCATE ON %s FOR EACH STATEMENT EXECUTE PROCEDURE "%s"."%s"();`,
			triggerName, d.sanitizeTableName(l.Table), d.Config.Schema, triggerFnName)
	}

	return statement, eventName, nil
}

func (d *Driver) getDeleteTriggerSqlForEvent(listenerUid string, l *types.ListenerConfig, e *types.Operation) (string, string, error) {
	uniqueName, err := d.getUniqueIdentifierForListenerEvent(listenerUid, e)
	if err != nil {
		return "", "", err
	}

	triggerFnName := uniqueName + "_fn"
	eventName := uniqueName + "_event"

	return fmt.Sprintf(`DROP FUNCTION IF EXISTS "%s"."%s" CASCADE;`, d.Config.Schema, triggerFnName), eventName, nil
}

func (d *Driver) getOperationNameForEvent(e *types.Operation) (string, error) {
	operation := ""
	switch *e {
	case types.OperationInsert:
		operation = "INSERT"
	case types.OperationUpdate:
		operation = "UPDATE"
	case types.OperationDelete:
		operation = "DELETE"
	case types.OperationTruncate:
		operation = "TRUNCATE"
	default:
		return "", errors.New("could not determine event type")
	}
	return operation, nil
}
func (d *Driver) getOperationFromName(operationName string) (types.Operation, error) {
	var event types.Operation

	switch strings.ToUpper(operationName) {
	case "INSERT":
		event = types.OperationInsert
	case "UPDATE":
		event = types.OperationUpdate
	case "DELETE":
		event = types.OperationDelete
	case "TRUNCATE":
		event = types.OperationTruncate
	default:
		return 0, errors.New("could not determine event type")
	}
	return event, nil
}
func (d *Driver) getUniqueIdentifierForListenerEvent(listenerUid string, e *types.Operation) (string, error) {
	operationName, err := d.getOperationNameForEvent(e)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		d.Config.Schema,
		listenerUid,
		strings.ToLower(operationName),
	}, "_"), nil
}
func (d *Driver) parseEventName(channel string) (string, types.Operation, error) {
	parts := strings.Split(channel, "_")
	if len(parts) != 4 {
		return "", 0, errors.New("could not determine unique identifier")
	}

	listenerUid := parts[1]
	operation, err := d.getOperationFromName(parts[2])
	if err != nil {
		return "", 0, err
	}

	return listenerUid, operation, nil

}
func (d *Driver) sanitizeTableName(tableName string) string {
	segments := strings.Split(tableName, ".")
	for i, segment := range segments {
		segments[i] = `"` + segment + `"`
	}
	return strings.Join(segments, ".")
}
func (d *Driver) sqlExec(conn *sql.DB, query string) (sql.Result, error) {
	d._clientConfig.Logger.Trace().Str("query", query).Msg("sending sql request")
	return conn.Exec(query)
}
