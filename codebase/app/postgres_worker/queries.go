package postgresworker

import (
	"database/sql"
	"fmt"

	"github.com/golangid/candi/logger"
)

const (
	eventsConst              = "events"
	notifyEventFunctionQuery = `CREATE OR REPLACE FUNCTION notify_event() RETURNS TRIGGER AS $$

	DECLARE 
		data json;
		notification json;
		
	BEGIN
		
		-- Convert the old or new row to JSON, based on the kind of action.
		CASE TG_OP
		WHEN 'INSERT' THEN
			data = json_build_object(
				'new', row_to_json(NEW)
			);
		WHEN 'DELETE' THEN
			data = json_build_object(
				'old', row_to_json(OLD)
			);
		ELSE
			data = json_build_object(
				'old', row_to_json(OLD),
				'new', row_to_json(NEW)
			);
		END CASE;

		IF LENGTH(data::text) >= 7900 THEN
			data = json_build_object(
				'is_too_long_payload', TRUE,
				'old_id', row_to_json(OLD)::jsonb->>'id',
				'new_id', row_to_json(NEW)::jsonb->>'id'
			);
		END IF;

		-- Construct the notification as a JSON string.
		notification = json_build_object(
						'event_id', md5(''||now()::text||random()::text),
						'table', TG_TABLE_NAME,
						'action', TG_OP,
						'data', data);
		
		-- Execute pg_notify(channel, notification)
		PERFORM pg_notify('events', notification::text);
		
		-- Result is ignored since this is an AFTER trigger
		RETURN NULL; 
	END;

$$ LANGUAGE plpgsql;`
)

type (
	// EventPayload event model
	EventPayload struct {
		EventID string           `json:"event_id"`
		Table   string           `json:"table"`
		Action  string           `json:"action"`
		Data    EventPayloadData `json:"data"`
	}
	// EventPayloadData event data
	EventPayloadData struct {
		IsTooLongPayload bool        `json:"is_too_long_payload,omitempty"`
		OldID            string      `json:"old_id"`
		NewID            string      `json:"new_id"`
		Old              interface{} `json:"old"`
		New              interface{} `json:"new"`
	}
)

// GetID get id if old/new data is empty, cause from long payload limitation
func (e EventPayload) GetID() string {
	if e.Data.IsTooLongPayload {
		if e.Data.NewID != "" {
			return e.Data.NewID
		}
		return e.Data.OldID
	}

	return ""
}

func execCreateFunctionEventQuery(db *sql.DB) {
	query := `SELECT pg_get_functiondef('notify_event()'::regprocedure);`
	var tmp string
	err := db.QueryRow(query).Scan(&tmp)
	if err != nil {
		stmt, err := db.Prepare(notifyEventFunctionQuery)
		if err != nil {
			logger.LogYellow("Postgres Listener: warning, cannot create notify_event function. Error: " + err.Error())
			return
		}
		defer stmt.Close()

		if _, err = stmt.Exec(); err != nil {
			panic(fmt.Errorf("failed when create event function: %s", err))
		}
	}
}

func execTriggerQuery(db *sql.DB, tableName string) {
	query := `SELECT event_object_table AS table_name
		FROM information_schema.triggers
		WHERE event_object_table=$1
		GROUP BY table_name`

	var existingTable string
	err := db.QueryRow(query, tableName).Scan(&existingTable)
	if err != nil {
		stmt, err := db.Prepare(`CREATE TRIGGER ` + tableName + `_notify_event
		AFTER INSERT OR UPDATE OR DELETE ON ` + tableName + `
		FOR EACH ROW EXECUTE PROCEDURE notify_event();`)
		if err != nil {
			logger.LogYellow("Postgres Listener: warning, cannot exec trigger for table " + tableName + ". Error: " + err.Error())
			return
		}
		defer stmt.Close()

		if _, err = stmt.Exec(); err != nil {
			panic(fmt.Errorf("failed when create trigger for table %s: %s", tableName, err))
		}
	}
}
