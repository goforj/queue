package redisqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisProtocolError string

// Error returns the Redis protocol error text.
func (e redisProtocolError) Error() string { return string(e) }

// RedisError marks the value as a Redis server response for go-redis fallback handling.
func (redisProtocolError) RedisError() {}

type redisCommandHook struct {
	values      map[string]string
	setArgs     []any
	acquireArgs [][]any
	evalScript  string
}

// DialHook preserves the default dial path, which command interception prevents these tests from reaching.
func (h *redisCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook provides deterministic Redis replies while retaining go-redis command construction and script fallback behavior.
func (h *redisCommandHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		switch typed := cmd.(type) {
		case *redis.StatusCmd:
			if strings.ToLower(cmd.Name()) != "set" || len(args) != 5 {
				return fmt.Errorf("unexpected Redis status command: %#v", args)
			}
			h.setArgs = append([]any(nil), args...)
			key, _ := args[1].(string)
			value, _ := args[2].(string)
			h.values[key] = value
			typed.SetVal("OK")
		case *redis.BoolCmd:
			if strings.ToLower(cmd.Name()) != "set" || len(args) != 6 {
				return fmt.Errorf("unexpected Redis boolean command: %#v", args)
			}
			h.acquireArgs = append(h.acquireArgs, append([]any(nil), args...))
			key, _ := args[1].(string)
			value, _ := args[2].(string)
			if _, exists := h.values[key]; exists {
				typed.SetVal(false)
			} else {
				h.values[key] = value
				typed.SetVal(true)
			}
		case *redis.StringCmd:
			if strings.ToLower(cmd.Name()) != "get" || len(args) != 2 {
				return fmt.Errorf("unexpected Redis string command: %#v", args)
			}
			key, _ := args[1].(string)
			value, exists := h.values[key]
			if !exists {
				return redis.Nil
			}
			typed.SetVal(value)
		case *redis.Cmd:
			switch strings.ToLower(cmd.Name()) {
			case "evalsha":
				return redisProtocolError("NOSCRIPT no matching script")
			case "eval":
				if len(args) != 5 {
					return fmt.Errorf("unexpected Redis eval command: %#v", args)
				}
				h.evalScript, _ = args[1].(string)
				key, _ := args[3].(string)
				token, _ := args[4].(string)
				if h.values[key] == token {
					delete(h.values, key)
					typed.SetVal(int64(1))
				} else {
					typed.SetVal(int64(0))
				}
			default:
				return fmt.Errorf("unexpected Redis command: %#v", args)
			}
		default:
			return fmt.Errorf("unexpected Redis command type %T", cmd)
		}
		return nil
	}
}

// ProcessPipelineHook preserves the default pipeline path because the state adapter issues standalone commands.
func (h *redisCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// TestRedisTimelineClientStateSemantics verifies timeline and uniqueness commands share one go-redis command implementation.
func TestRedisTimelineClientStateSemantics(t *testing.T) {
	hook := &redisCommandHook{values: make(map[string]string)}
	client := redis.NewClient(&redis.Options{Addr: "redis.invalid:6379"})
	client.AddHook(hook)
	state := &redisTimelineClient{client: client}
	ctx := context.Background()

	if err := state.Set(ctx, "timeline", "sample", time.Minute); err != nil {
		t.Fatalf("set timeline sample: %v", err)
	}
	if args := hook.setArgs; len(args) != 5 || args[0] != "set" || args[1] != "timeline" || args[2] != "sample" || args[3] != "ex" || args[4] != int64(60) {
		t.Fatalf("timeline SET arguments = %#v, want expiring 60-second command", args)
	}
	if got, err := state.Get(ctx, "timeline"); err != nil || got != "sample" {
		t.Fatalf("get timeline sample = %q, %v, want sample", got, err)
	}

	acquired, err := state.Acquire(ctx, "claim", "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire initial claim = %t, %v, want true", acquired, err)
	}
	acquired, err = state.Acquire(ctx, "claim", "owner-b", time.Minute)
	if err != nil || acquired {
		t.Fatalf("acquire competing claim = %t, %v, want false", acquired, err)
	}
	if len(hook.acquireArgs) != 2 {
		t.Fatalf("claim SET commands = %d, want 2", len(hook.acquireArgs))
	}
	for index, owner := range []string{"owner-a", "owner-b"} {
		args := hook.acquireArgs[index]
		if len(args) != 6 || args[0] != "set" || args[1] != "claim" || args[2] != owner || args[3] != "ex" || args[4] != int64(60) || args[5] != "nx" {
			t.Fatalf("claim SET arguments %d = %#v, want owner-scoped 60-second NX command", index, args)
		}
	}

	if err := state.Release(ctx, "claim", "owner-b"); err != nil {
		t.Fatalf("release non-owner claim: %v", err)
	}
	if got, err := client.Get(ctx, "claim").Result(); err != nil || got != "owner-a" {
		t.Fatalf("claim after non-owner release = %q, %v, want owner-a", got, err)
	}
	if err := state.Release(ctx, "claim", "owner-a"); err != nil {
		t.Fatalf("release owned claim: %v", err)
	}
	if _, err := state.Get(ctx, "claim"); !errors.Is(err, redis.Nil) {
		t.Fatalf("released claim lookup = %v, want redis.Nil", err)
	}
	if !strings.Contains(hook.evalScript, `redis.call("GET", KEYS[1]) == ARGV[1]`) || !strings.Contains(hook.evalScript, `redis.call("DEL", KEYS[1])`) {
		t.Fatalf("release script does not preserve token-checked deletion: %q", hook.evalScript)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close state client: %v", err)
	}
}
