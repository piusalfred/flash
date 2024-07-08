package wal

import (
	"fmt"
	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/quix-labs/flash/pkg/types"
)

func (d *Driver) processXld(xld *pglogrepl.XLogData) (bool, error) {
	logicalMsg, err := pglogrepl.ParseV2(xld.WALData, d.replicationState.inStream)
	if err != nil {
		return false, err
	}

	d.replicationState.lastReceivedLSN = xld.ServerWALEnd
	return d.processMessage(logicalMsg, false)
}

func (d *Driver) processMessage(logicalMsg pglogrepl.Message, fromQueue bool) (bool, error) {
	switch logicalMsg := logicalMsg.(type) {
	case *pglogrepl.RelationMessageV2:
		d.replicationState.relations[logicalMsg.RelationID] = logicalMsg

	case *pglogrepl.BeginMessage:
		if d.replicationState.lastWrittenLSN > logicalMsg.FinalLSN {
			d._clientConfig.Logger.Trace().Msgf("Received stale message, ignoring. Last written LSN: %s Message LSN: %s", d.replicationState.lastWrittenLSN, logicalMsg.FinalLSN)
			d.replicationState.processMessages = false
			break
		}

		d.replicationState.processMessages = true
		d.replicationState.currentTransactionLSN = logicalMsg.FinalLSN

	case *pglogrepl.CommitMessage:
		d.replicationState.processMessages = false
		return true, nil

	case *pglogrepl.InsertMessageV2:
		// If we are in replicationState, append XLogData to memory to run/delete after stream commit/abort
		if d.replicationState.inStream && !fromQueue {
			d.replicationState.streamQueues[logicalMsg.Xid] = append(d.replicationState.streamQueues[logicalMsg.Xid], logicalMsg)
			break
		}

		if !d.replicationState.processMessages && !fromQueue {
			// Stale message
			break
		}

		tableName, _ := d.getRelationTableName(logicalMsg.RelationID)
		listeners, exists := d.activeListeners[tableName]
		if !exists {
			break
		}

		newData, err := d.parseTuple(logicalMsg.RelationID, logicalMsg.Tuple)
		if err != nil {
			return false, err
		}
		for listenerUid, _ := range listeners {
			//TODO EXTRACT ONLY SPECIFIED FIELDS
			*d.eventsChan <- &types.DatabaseEvent{
				ListenerUid: listenerUid,
				Event:       &types.InsertEvent{New: newData},
			}
		}

	case *pglogrepl.UpdateMessageV2:
		// If we are in replicationState, append XLogData to memory to run/delete after stream commit/abort
		if d.replicationState.inStream && !fromQueue {
			d.replicationState.streamQueues[logicalMsg.Xid] = append(d.replicationState.streamQueues[logicalMsg.Xid], logicalMsg)
			break
		}

		if !d.replicationState.processMessages && !fromQueue {
			// Stale message
			break
		}

		tableName, _ := d.getRelationTableName(logicalMsg.RelationID)
		listeners, exists := d.activeListeners[tableName]
		if !exists {
			break
		}

		newData, err := d.parseTuple(logicalMsg.RelationID, logicalMsg.NewTuple)
		if err != nil {
			return false, err
		}

		oldData, err := d.parseTuple(logicalMsg.RelationID, logicalMsg.OldTuple)
		if err != nil {
			return false, err
		}
		for listenerUid, _ := range listeners {
			//TODO EXTRACT ONLY SPECIFIED FIELDS
			//TODO ONLY IF EVENT IS LISTENED, ACTUALLY ALWAYS SEND
			// TODO ONLY IF FIELDS CHANGED - POSTGRES >15 ALLOW WHERE ON PUBLICATION
			// @link https://www.postgresql.org/docs/current/sql-alterpublication.html
			*d.eventsChan <- &types.DatabaseEvent{
				ListenerUid: listenerUid,
				Event:       &types.UpdateEvent{Old: oldData, New: newData},
			}
		}

	case *pglogrepl.DeleteMessageV2:
		// If we are in replicationState, append XLogData to memory to run/delete after stream commit/abort
		if d.replicationState.inStream && !fromQueue {
			d.replicationState.streamQueues[logicalMsg.Xid] = append(d.replicationState.streamQueues[logicalMsg.Xid], logicalMsg)
			break
		}

		if !d.replicationState.processMessages && !fromQueue {
			// Stale message
			break
		}

		tableName, _ := d.getRelationTableName(logicalMsg.RelationID)
		listeners, exists := d.activeListeners[tableName]
		if !exists {
			break
		}
		oldData, err := d.parseTuple(logicalMsg.RelationID, logicalMsg.OldTuple)
		if err != nil {
			return false, err
		}
		for listenerUid, _ := range listeners {
			//TODO EXTRACT ONLY SPECIFIED FIELDS
			*d.eventsChan <- &types.DatabaseEvent{
				ListenerUid: listenerUid,
				Event:       &types.DeleteEvent{Old: oldData},
			}
		}

	case *pglogrepl.TruncateMessageV2:
		// If we are in replicationState, append XLogData to memory to run/delete after stream commit/abort
		if d.replicationState.inStream && !fromQueue {
			d.replicationState.streamQueues[logicalMsg.Xid] = append(d.replicationState.streamQueues[logicalMsg.Xid], logicalMsg)
			break
		}

		if !d.replicationState.processMessages && !fromQueue {
			// Stale message
			break
		}

		for _, relId := range logicalMsg.RelationIDs {
			tableName, _ := d.getRelationTableName(relId)
			listeners, exists := d.activeListeners[tableName]
			if !exists {
				break
			}
			for listenerUid, _ := range listeners {
				*d.eventsChan <- &types.DatabaseEvent{
					ListenerUid: listenerUid,
					Event:       &types.TruncateEvent{},
				}
			}
		}
	case *pglogrepl.TypeMessageV2:
		d._clientConfig.Logger.Trace().Msgf("typeMessage for xid %d\n", logicalMsg.Xid)
	case *pglogrepl.OriginMessage:
		d._clientConfig.Logger.Trace().Msgf("originMessage for xid %s\n", logicalMsg.Name)
	case *pglogrepl.LogicalDecodingMessageV2:
		d._clientConfig.Logger.Trace().Msgf("Logical decoding message: %q, %q, %d", logicalMsg.Prefix, logicalMsg.Content, logicalMsg.Xid)

	case *pglogrepl.StreamStartMessageV2:
		d.replicationState.inStream = true
		// Create dynamic queue if not exists
		if _, exists := d.replicationState.streamQueues[logicalMsg.Xid]; !exists {
			d.replicationState.streamQueues[logicalMsg.Xid] = []pglogrepl.Message{} // Dynamic size
		}
		d._clientConfig.Logger.Trace().Msgf("Stream start message: xid %d, first segment? %d", logicalMsg.Xid, logicalMsg.FirstSegment)

	case *pglogrepl.StreamStopMessageV2:
		d.replicationState.inStream = false
		d._clientConfig.Logger.Trace().Msgf("Stream stop message")
	case *pglogrepl.StreamCommitMessageV2:
		d._clientConfig.Logger.Trace().Msgf("Stream commit message: xid %d", logicalMsg.Xid)

		// Process all events then remove queue
		queueLen := len(d.replicationState.streamQueues[logicalMsg.Xid])
		if queueLen > 0 {
			d._clientConfig.Logger.Trace().Msgf("Processing %d entries from stream queue: xid %d", queueLen, logicalMsg.Xid)
			// ⚠️ Do not use goroutine to handle in parallel, order is very important
			for _, message := range d.replicationState.streamQueues[logicalMsg.Xid] {
				// Cannot flush position here because return statement can cause loss
				_, err := d.processMessage(message, true)
				if err != nil {
					return false, err
				}
			}
		}
		d._clientConfig.Logger.Trace().Msgf("Delete %d entries from stream queue: xid %d", queueLen, logicalMsg.Xid)
		delete(d.replicationState.streamQueues, logicalMsg.Xid)
		return true, nil // FLUSH position

	case *pglogrepl.StreamAbortMessageV2:
		d._clientConfig.Logger.Trace().Msgf("Stream abort message: xid %d", logicalMsg.Xid)
		d._clientConfig.Logger.Trace().Msgf("Delete %d entries from stream queue: xid %d", len(d.replicationState.streamQueues[logicalMsg.Xid]), logicalMsg.Xid)
		delete(d.replicationState.streamQueues, logicalMsg.Xid)
	default:
		d._clientConfig.Logger.Trace().Msgf("Unknown message type in pgoutput stream: %T", logicalMsg)
	}

	return false, nil
}

func (d *Driver) parseTuple(relationID uint32, tuple *pglogrepl.TupleData) (*types.EventData, error) {
	rel, ok := d.replicationState.relations[relationID]
	if !ok {
		return nil, fmt.Errorf("unknown relation ID %d", relationID)
	}
	if len(tuple.Columns) == 0 {
		return nil, nil
	}
	values := types.EventData{} //Initialize as nil and create only on first col
	for idx, col := range tuple.Columns {
		colName := rel.Columns[idx].Name
		switch col.DataType {
		case 'n': // null
			values[colName] = nil
		case 'u': // unchanged toast
			// This TOAST value was not changed. TOAST values are not stored in the tuple, and logical replication doesn't want to spend a disk read to fetch its value for you.
		case 't': //text
			val, err := d.decodeTextColumnData(col.Data, rel.Columns[idx].DataType)
			if err != nil {
				return nil, err
			}
			values[colName] = val
		}
	}
	return &values, nil
}

func (d *Driver) getRelationTableName(relationID uint32) (string, error) {
	rel, ok := d.replicationState.relations[relationID]
	if !ok {
		return "", fmt.Errorf("unknown relation ID %d", relationID)
	}
	return rel.Namespace + "." + rel.RelationName, nil
}

func (d *Driver) decodeTextColumnData(data []byte, dataType uint32) (interface{}, error) {
	if dt, ok := d.replicationState.typeMap.TypeForOID(dataType); ok {
		return dt.Codec.DecodeValue(d.replicationState.typeMap, dataType, pgtype.TextFormatCode, data)
	}
	return string(data), nil
}
