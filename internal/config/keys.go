package config

import "strings"

// cacheSchemaVersion is the `v1` segment in every cache key. Bump it in code when
// the encoded cache shape (codec.go) changes so entries written under the old
// shape are never decoded under a new reader ([[caching-strategy]] versioned
// keys). It also namespaces the pub/sub invalidation channel.
const cacheSchemaVersion = "v1"

// invalidateChannel is the Valkey pub/sub channel every engine instance
// subscribes to at startup. On publish/rollback the writer deletes its matching
// keys AND publishes here so the whole fleet drops its copies (R5, AC-16).
const invalidateChannel = "cfg:" + cacheSchemaVersion + ":invalidate"

// keyFlow builds the namespaced+versioned cache key for a resolved flow version.
//
//	cfg:v1:flow:{env}:{method}:{path}
func keyFlow(env, method, path string) string {
	return strings.Join([]string{"cfg", cacheSchemaVersion, "flow", env, method, path}, ":")
}

// keyJDM builds the cache key for a JDM:
//
//	cfg:v1:jdm:{env}:{jdmID}
func keyJDM(env, id string) string {
	return strings.Join([]string{"cfg", cacheSchemaVersion, "jdm", env, id}, ":")
}

// keyConns builds the cache key for the whole-env connection list:
//
//	cfg:v1:conns:{env}
func keyConns(env string) string {
	return strings.Join([]string{"cfg", cacheSchemaVersion, "conns", env}, ":")
}

// flowDeletePattern is the glob matching every cached flow key in env, used by
// Invalidate on a publish/rollback (a pointer move can change any route the flow
// owns, so the whole env's flow namespace is dropped — bounded, cheap, and
// correctness-first).
func flowDeletePattern(env string) string {
	return strings.Join([]string{"cfg", cacheSchemaVersion, "flow", env, "*"}, ":")
}

// jdmDeletePattern matches a specific JDM's key for targeted invalidation.
func jdmDeletePattern(env, id string) string {
	return keyJDM(env, id)
}

// connsDeletePattern matches the env's connection-list key.
func connsDeletePattern(env string) string {
	return keyConns(env)
}
