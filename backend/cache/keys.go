package cache

// 键前缀集中在此处，避免各仓储各写一份导致失效时对不上。
const keyPrefix = "msu:"

func AccountKey(name string) string { return keyPrefix + "account:" + name }

func ModelKey(id string) string { return keyPrefix + "model:" + id }
