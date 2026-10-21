package xrayconfig

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/transaction"
)

func editableOutbound(tag string) error {
	if err := validateOutboundTag(tag); err != nil {
		return err
	}
	if _, protected := protectedOutboundTag[tag]; protected && tag != "proxy" {
		return transaction.Reject("outbound_protected", "system outbound cannot be edited in the connection editor", 403)
	}
	return nil
}

// OutboundEdit returns only the explicitly selected connection, including credentials.
func (reader Reader) OutboundEdit(ctx context.Context, tag string) (api.OutboundEditResponse, error) {
	if err := editableOutbound(tag); err != nil {
		return api.OutboundEditResponse{}, err
	}
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.OutboundEditResponse{}, err
	}
	for _, item := range asSlice(doc.value["outbounds"]) {
		value := asMap(item)
		if value["tag"] == tag {
			raw, err := json.Marshal(value)
			return api.OutboundEditResponse{ConfigMetadata: doc.metadata, Outbound: raw}, err
		}
	}
	return api.OutboundEditResponse{}, transaction.Reject("outbound_not_found", "outbound was not found", 404)
}

func (reader Reader) OutboundReferences(ctx context.Context, tag string) (api.OutboundReferencesResponse, error) {
	doc, err := reader.readDocument(ctx)
	if err != nil {
		return api.OutboundReferencesResponse{}, err
	}
	found := false
	for _, item := range asSlice(doc.value["outbounds"]) {
		if asMap(item)["tag"] == tag {
			found = true
			break
		}
	}
	if !found {
		return api.OutboundReferencesResponse{}, transaction.Reject("outbound_not_found", "outbound was not found", 404)
	}
	_, protected := protectedOutboundTag[tag]
	return api.OutboundReferencesResponse{ConfigMetadata: doc.metadata, Tag: tag, Protected: protected, References: outboundReferences(doc.value, tag)}, nil
}

func decodeNativeOutbound(raw json.RawMessage) (map[string]any, error) {
	var typed conf.OutboundDetourConfig
	var value map[string]any
	if json.Unmarshal(raw, &typed) != nil || json.Unmarshal(raw, &value) != nil || value == nil || typed.Protocol == "" {
		return nil, transaction.Reject("invalid_outbound", "one native Xray outbound object is required", 400)
	}
	// UseNumber avoids rounding values retained from the existing configuration.
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, transaction.Reject("invalid_outbound", "outbound is invalid", 400)
	}
	return value, nil
}

// PlanWriteOutbound has three distinct operations: create, edit and replace.
// Edit never changes identity or protocol. Replace discards old connection options.
func PlanWriteOutbound(tag string, raw json.RawMessage, operation string) transaction.Planner {
	return func(source []byte) ([]transaction.ByteEdit, error) {
		candidate, err := decodeNativeOutbound(raw)
		if err != nil {
			return nil, err
		}
		if operation != "create" && operation != "edit" && operation != "replace" {
			return nil, transaction.Reject("invalid_request", "invalid outbound operation", 400)
		}
		if err := editableOutbound(tag); err != nil {
			return nil, err
		}
		e, err := newEditor(source)
		if err != nil {
			return nil, err
		}
		array, err := outboundsArray(e.syntax)
		if err != nil {
			return nil, err
		}
		index, current := findOutbound(e.syntax, tag)
		if operation == "create" {
			if _, reserved := protectedOutboundTag[tag]; reserved {
				return nil, transaction.Reject("outbound_protected", "system outbound tag is reserved", 403)
			}
			if index >= 0 {
				return nil, transaction.Reject("outbound_exists", "outbound already exists", 409)
			}
		} else if index < 0 {
			return nil, transaction.Reject("outbound_not_found", "outbound was not found", 404)
		}
		if supplied, exists := candidate["tag"]; exists && supplied != tag {
			return nil, transaction.Reject("outbound_identity_changed", "outbound tag cannot be changed", 400)
		}
		if operation == "edit" && candidate["protocol"] != current["protocol"] {
			return nil, transaction.Reject("outbound_protocol_changed", "use explicit connection replacement to change protocol", 400)
		}
		candidate["tag"] = tag
		// Only the mandatory mark is inherited; no sockopt, mux or transport merging on replacement.
		mark, err := nativeEgressMark(e.syntax.value, current)
		if err != nil {
			return nil, err
		}
		if mark != nil {
			stream, _ := candidate["streamSettings"].(map[string]any)
			if stream == nil {
				stream = map[string]any{}
				candidate["streamSettings"] = stream
			}
			sockopt, _ := stream["sockopt"].(map[string]any)
			if sockopt == nil {
				sockopt = map[string]any{}
				stream["sockopt"] = sockopt
			}
			sockopt["mark"] = mark
		}
		if operation == "create" {
			edit, err := e.appendArrayItem(array, candidate)
			return []transaction.ByteEdit{edit}, err
		}
		// Existing raw JSONC remains authoritative. Only changed values are written.
		work := newSession(source)
		if err := planNativeDiff(work, []string{"outbounds", stringIndex(index)}, current, candidate); err != nil {
			return nil, err
		}
		return work.edits(), nil
	}
}

func nativeEgressMark(config map[string]any, current map[string]any) (any, error) {
	if mark, ok := asMap(asMap(current["streamSettings"])["sockopt"])["mark"]; ok {
		return mark, nil
	}
	var mark any
	for _, item := range asSlice(config["outbounds"]) {
		value, ok := asMap(asMap(asMap(item)["streamSettings"])["sockopt"])["mark"]
		if !ok {
			continue
		}
		n, valid := markInteger(value)
		if !valid {
			return nil, transaction.Reject("egress_mark_ambiguous", "native egress mark is ambiguous", 422)
		}
		if mark != nil {
			m, _ := markInteger(mark)
			if n != m {
				return nil, transaction.Reject("egress_mark_ambiguous", "native egress mark is ambiguous", 422)
			}
		}
		mark = value
	}
	if mark == nil {
		return nil, transaction.Reject("egress_mark_ambiguous", "native egress mark is unavailable", 422)
	}
	return mark, nil
}

func stringIndex(index int) string { raw, _ := json.Marshal(index); return string(raw) }

func nodeAt(root *jsonNode, path []string) *jsonNode {
	node := root
	for _, part := range path {
		if node == nil {
			return nil
		}
		if node.object != nil {
			node = node.object[part]
		} else {
			var index int
			if json.Unmarshal([]byte(part), &index) != nil || index < 0 || index >= len(node.array) {
				return nil
			}
			node = node.array[index]
		}
	}
	return node
}

func planNativeDiff(work *session, path []string, before, after any) error {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	oldObject, oldOK := before.(map[string]any)
	newObject, newOK := after.(map[string]any)
	if oldOK && newOK {
		keys := make([]string, 0, len(oldObject)+len(newObject))
		seen := map[string]bool{}
		for key := range oldObject {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range newObject {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			old, hasOld := oldObject[key]
			next, hasNext := newObject[key]
			if hasOld && hasNext {
				if err := planNativeDiff(work, appendPath(path, key), old, next); err != nil {
					return err
				}
				continue
			}
			err := work.apply(func(e *editor) ([]transaction.ByteEdit, error) {
				parent := nodeAt(e.syntax.spans, path)
				if !hasNext {
					return e.deleteMembers(parent, map[string]bool{key: true}), nil
				}
				edit, err := e.appendObjectMember(parent, key, next)
				return []transaction.ByteEdit{edit}, err
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	oldArray, oldOK := before.([]any)
	newArray, newOK := after.([]any)
	if oldOK && newOK && len(oldArray) == len(newArray) {
		for index := range oldArray {
			if err := planNativeDiff(work, appendPath(path, stringIndex(index)), oldArray[index], newArray[index]); err != nil {
				return err
			}
		}
		return nil
	}
	return work.apply(func(e *editor) ([]transaction.ByteEdit, error) {
		edit, err := e.replaceNode(nodeAt(e.syntax.spans, path), after)
		return []transaction.ByteEdit{edit}, err
	})
}
func appendPath(path []string, part string) []string {
	result := append([]string(nil), path...)
	return append(result, part)
}
