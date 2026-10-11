package model

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aceaura/model-surge-upstream/backend/account"
	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/cache"
	"github.com/aceaura/model-surge-upstream/backend/credential"
	"github.com/aceaura/model-surge-upstream/backend/internal/testenv"
	"github.com/aceaura/model-surge-upstream/backend/provider"
)

// lookupOf 用 account 仓储构造 AccountLookup，与生产装配方式一致。
func lookupOf(repo *account.Repo) AccountLookup {
	return func(ctx context.Context, name string) (provider.Spec, error) {
		acc, err := repo.Get(ctx, name)
		if err != nil {
			return provider.Spec{}, err
		}
		spec, ok := acc.Spec()
		if !ok {
			return provider.Spec{}, apperr.New(apperr.InvalidProvider,
				fmt.Sprintf("account %q references unknown provider %q", name, acc.ProviderID))
		}
		return spec, nil
	}
}

func fixtures(t *testing.T) (*Repo, *account.Repo) {
	t.Helper()
	s := testenv.Store(t)
	c := testenv.Cache(t)
	accounts := account.NewRepo(s.Pool(), c)
	if _, err := accounts.Create(context.Background(), account.Input{
		Name:       "kimi-1",
		ProviderID: "kimi.global.subscribe.coding",
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-kimi-secret"},
		Enabled:    true,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return NewRepo(s.Pool(), c, lookupOf(accounts)), accounts
}

func input() Input {
	return Input{
		ID:            "kimi-1/k2",
		Account:       "kimi-1",
		NativeModel:   "kimi-k2-turbo",
		Protocol:      provider.ProtocolAnthropic,
		ContextWindow: 262144,
		Enabled:       true,
	}
}

func TestNormalizeObject(t *testing.T) {
	for _, in := range []json.RawMessage{nil, {}, json.RawMessage(`null`), json.RawMessage(`{}`)} {
		got, err := normalizeObject(in, "defaults")
		if err != nil {
			t.Fatalf("normalizeObject(%s): %v", in, err)
		}
		if string(got) != `{}` {
			t.Errorf("normalizeObject(%s) = %s, want {}", in, got)
		}
	}
	for _, in := range []string{`[1,2]`, `"str"`, `7`, `{oops}`} {
		if _, err := normalizeObject(json.RawMessage(in), "overrides"); err == nil {
			t.Errorf("normalizeObject(%s) should fail", in)
		} else if !apperr.Is(err, apperr.InvalidJSON) {
			t.Errorf("normalizeObject(%s) code = %q", in, apperr.CodeOf(err))
		}
	}
}

func TestCreateAndGet(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, input())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if string(created.Defaults) != `{}` || string(created.Overrides) != `{}` {
		t.Errorf("empty params should normalize to {}: %+v", created)
	}

	got, err := repo.Get(ctx, "kimi-1/k2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NativeModel != "kimi-k2-turbo" || got.ContextWindow != 262144 || got.Protocol != provider.ProtocolAnthropic {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestCreateWithParams(t *testing.T) {
	repo, _ := fixtures(t)
	in := input()
	in.Defaults = json.RawMessage(`{"temperature":0.6}`)
	in.Overrides = json.RawMessage(`{"max_tokens":8192}`)

	created, err := repo.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var defaults, overrides map[string]any
	if err := json.Unmarshal(created.Defaults, &defaults); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(created.Overrides, &overrides); err != nil {
		t.Fatal(err)
	}
	if defaults["temperature"] != 0.6 || overrides["max_tokens"] != float64(8192) {
		t.Errorf("params not persisted: %v %v", defaults, overrides)
	}
}

// 清除配置(strip):创建落库回读一致;Update 缺省保留旧值、显式数组覆盖、
// 空数组清空;非法路径(空段/非数组)保存期拒绝。
func TestStripConfig(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	in := input()
	in.Strip = json.RawMessage(`["store","metadata.user_id"]`)
	created, err := repo.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !slices.Equal(created.Strip, []string{"store", "metadata.user_id"}) {
		t.Fatalf("strip = %v", created.Strip)
	}
	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !slices.Equal(got.Strip, []string{"store", "metadata.user_id"}) {
		t.Fatalf("round trip strip = %v", got.Strip)
	}

	// Update 缺省(不带 strip 键)保留旧值。
	updated, err := repo.Update(ctx, Input{ID: in.ID, EffortEnabled: true, Enabled: true})
	if err != nil {
		t.Fatalf("update keep: %v", err)
	}
	if !slices.Equal(updated.Strip, []string{"store", "metadata.user_id"}) {
		t.Fatalf("update should keep strip, got %v", updated.Strip)
	}
	// 显式空数组清空。
	updated, err = repo.Update(ctx, Input{ID: in.ID, Strip: json.RawMessage(`[]`), EffortEnabled: true, Enabled: true})
	if err != nil {
		t.Fatalf("update clear: %v", err)
	}
	if len(updated.Strip) != 0 {
		t.Fatalf("update should clear strip, got %v", updated.Strip)
	}

	for _, bad := range []string{`["a..b"]`, `[""]`, `["."]`, `{"a":1}`, `"store"`} {
		badIn := input()
		badIn.ID = "kimi-1/bad-strip"
		badIn.Strip = json.RawMessage(bad)
		if _, err := repo.Create(ctx, badIn); err == nil {
			t.Errorf("strip %s should be rejected", bad)
		}
	}
	// 路径整体 trim 后落库。
	trimIn := input()
	trimIn.ID = "kimi-1/trim-strip"
	trimIn.Strip = json.RawMessage(`[" store "]`)
	trimmed, err := repo.Create(ctx, trimIn)
	if err != nil {
		t.Fatalf("create trim: %v", err)
	}
	if !slices.Equal(trimmed.Strip, []string{"store"}) {
		t.Errorf("strip should be trimmed, got %v", trimmed.Strip)
	}
}

func TestCreateRejects(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	cases := map[string]struct {
		mutate func(*Input)
		code   apperr.Code
	}{
		"empty id":          {func(in *Input) { in.ID = "" }, apperr.InvalidRequest},
		"empty account":     {func(in *Input) { in.Account = "" }, apperr.InvalidRequest},
		"empty native":      {func(in *Input) { in.NativeModel = "" }, apperr.InvalidRequest},
		"negative window":   {func(in *Input) { in.ContextWindow = -1 }, apperr.InvalidRequest},
		"missing account":   {func(in *Input) { in.Account = "ghost" }, apperr.NotFound},
		"bad protocol":      {func(in *Input) { in.Protocol = provider.ProtocolResponses }, apperr.InvalidProtocol},
		"empty protocol":    {func(in *Input) { in.Protocol = "" }, apperr.InvalidProtocol},
		"bad defaults json": {func(in *Input) { in.Defaults = json.RawMessage(`[1]`) }, apperr.InvalidJSON},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := input()
			tc.mutate(&in)
			_, err := repo.Create(ctx, in)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := apperr.CodeOf(err); got != tc.code {
				t.Errorf("code = %q, want %q (err: %v)", got, tc.code, err)
			}
		})
	}
}

// TestCreateAllowsCrossFamilyFormats 同族约束已撤销(2026-10-11):双端格式
// 词表全量可用,跨族组合合法(承接「协议外壳+自家字段」的厂商差异);
// 词表外值仍按 ValidFormat/ValidEntryFormat 拒。
func TestCreateAllowsCrossFamilyFormats(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	str := func(s string) *string { return &s }
	accepts := []struct {
		name       string
		format, in *string
	}{
		{"空串放行", nil, nil},
		{"同族格式", str("anthropic_budget"), str("anthropic_adaptive")},
		{"旧值归一", str("anthropic"), nil},
		{"auto 入口放行", nil, str("")},
		{"effort_format 跨族", str("openai_chat"), nil},
		{"effort_in 跨族", nil, str("openai_chat")},
		{"effort_format gemini 跨族", str("gemini_level"), nil},
	}
	for i, tc := range accepts {
		t.Run(tc.name, func(t *testing.T) {
			in := input() // anthropic 协议
			in.ID = fmt.Sprintf("kimi-1/xfam-%d", i)
			in.EffortFormat, in.EffortIn = tc.format, tc.in
			if _, err := repo.Create(ctx, in); err != nil {
				t.Fatalf("跨族应放行: %v", err)
			}
		})
	}

	rejects := map[string]struct{ format, in *string }{
		"effort_format 词表外":   {format: str("bogus")},
		"effort_in 词表外":       {in: str("bogus")},
		"effort_in gemini 不收": {in: str("gemini_level")},
	}
	for name, tc := range rejects {
		t.Run(name, func(t *testing.T) {
			in := input()
			in.EffortFormat, in.EffortIn = tc.format, tc.in
			_, err := repo.Create(ctx, in)
			if err == nil || apperr.CodeOf(err) != apperr.InvalidRequest {
				t.Fatalf("词表外应拒 InvalidRequest, got %v", err)
			}
		})
	}
}

func TestProtocolErrorListsSupported(t *testing.T) {
	repo, _ := fixtures(t)
	in := input()
	in.Protocol = provider.ProtocolResponses
	_, err := repo.Create(context.Background(), in)
	if err == nil {
		t.Fatal("expected error")
	}
	spec, _ := provider.Get("kimi.global.subscribe.coding")
	for _, p := range spec.Protocols {
		if !contains(err.Error(), p) {
			t.Errorf("error %q should list supported protocol %q", err, p)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestCreateDuplicate(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Create(ctx, input())
	if !apperr.Is(err, apperr.AlreadyExists) {
		t.Errorf("err = %v, want already_exists", err)
	}
}

func TestUpdate(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	created, err := repo.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}

	in := input()
	in.Enabled = false
	in.ContextWindow = 131072
	in.Overrides = json.RawMessage(`{"thinking":{"budget_tokens":65535}}`)
	updated, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Enabled || updated.ContextWindow != 131072 {
		t.Errorf("update did not apply: %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Error("created_at should be preserved")
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.ContextWindow != 131072 {
		t.Errorf("cache and db disagree: %+v", got)
	}
}

func TestUpdateKeepsOmittedFields(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Update(ctx, Input{ID: "kimi-1/k2", Enabled: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Account != "kimi-1" || updated.NativeModel != "kimi-k2-turbo" || updated.Protocol != provider.ProtocolAnthropic {
		t.Errorf("omitted fields should be preserved: %+v", updated)
	}
}

func TestEffortsRoundTrip(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()

	// 未配置 efforts:落库存 null(自动=跟随上游声明)。有效列表不在仓储
	// 现算——由能访问上游的 httpapi/resolve 层填充,仓储只保证原始配置
	// 的持久化与显式数组的形态校验。
	in := input()
	in.ID = "kimi-1/gpt5"
	in.NativeModel = "gpt-5"
	in.Protocol = provider.ProtocolChatCompletions
	created, err := repo.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	assertRawEfforts(t, created.Efforts, `null`)

	// 显式名+值条目:原样落库(去重/迁移在消费侧现算);双端格式均 auto
	// 时词表校验跳过(见 TestEffortPairValidation 的显式格式校验)。
	in.Efforts = json.RawMessage(`[{"name":"高","value":"high"},{"name":"私有","value":"ultra"}]`)
	updated, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("update explicit: %v", err)
	}
	assertRawEfforts(t, updated.Efforts, `[{"name":"高","value":"high"},{"name":"私有","value":"ultra"}]`)

	// 更新不带 efforts:保留现状。
	keep, err := repo.Update(ctx, Input{ID: in.ID, Enabled: true})
	if err != nil {
		t.Fatalf("update omitted: %v", err)
	}
	assertRawEfforts(t, keep.Efforts, `[{"name":"高","value":"high"},{"name":"私有","value":"ultra"}]`)

	// 显式空数组 = 管理员声明不支持。
	in.Efforts = json.RawMessage(`[]`)
	empty, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("update empty: %v", err)
	}
	assertRawEfforts(t, empty.Efforts, `[]`)

	// null 恢复自动(跟随上游声明)。
	in.Efforts = json.RawMessage(`null`)
	auto, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("update null: %v", err)
	}
	assertRawEfforts(t, auto.Efforts, `null`)

	// 读回(走缓存/库)原始配置不变。
	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRawEfforts(t, got.Efforts, `null`)

	// 空 value 条目被拒(写路径仍校验显式数组形态)。
	in.Efforts = json.RawMessage(`[{"name":"空值","value":""}]`)
	if _, err := repo.Update(ctx, in); !apperr.Is(err, apperr.InvalidRequest) {
		t.Errorf("empty value err = %v, want invalid_request", err)
	}
}

func assertRawEfforts(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if string(got) != want {
		t.Fatalf("efforts = %s, want %s", got, want)
	}
}

// TestEffortPairValidation 映射对保存校验(2026-10-11 行式映射对):name 须
// 在下游格式词表内(下游 budget 时为正整数阈值串),value 须在上游格式词表
// 内(上游 budget 时为正整数串或 none),大小写不敏感;任一端格式 auto
// (词表运行时定)该端跳过;anthropic_off 上游显式声明须为空数组。
func TestEffortPairValidation(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	str := func(s string) *string { return &s }

	cases := []struct {
		name         string
		format, in   *string
		efforts      string
		wantRejected bool
	}{
		{"档到档合法", str("anthropic_effort"), str("openai_chat"), `[{"name":"low","value":"high"}]`, false},
		{"大小写不敏感", str("anthropic_effort"), str("openai_chat"), `[{"name":"LOW","value":"XHIGH"}]`, false},
		{"name 词表外", str("anthropic_effort"), str("openai_chat"), `[{"name":"bogus","value":"high"}]`, true},
		{"value 词表外", str("anthropic_effort"), str("openai_chat"), `[{"name":"low","value":"bogus"}]`, true},
		{"上游 auto 跳过 value 校验", nil, str("openai_chat"), `[{"name":"low","value":"ultra"}]`, false},
		{"下游 budget 阈值串合法", str("gemini_budget"), str("anthropic_budget"), `[{"name":"1024","value":"2048"}]`, false},
		{"下游 budget name 非数字", str("gemini_budget"), str("anthropic_budget"), `[{"name":"low","value":"2048"}]`, true},
		{"上游 budget value none 合法", str("gemini_budget"), str("anthropic_budget"), `[{"name":"1024","value":"none"}]`, false},
		{"上游 budget value 非数字非 none", str("gemini_budget"), str("anthropic_budget"), `[{"name":"1024","value":"high"}]`, true},
		{"anthropic_off 显式声明须空", str("anthropic_off"), str("openai_chat"), `[{"name":"low","value":"low"}]`, true},
		{"anthropic_off 空数组放行", str("anthropic_off"), str("openai_chat"), `[]`, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input()
			in.ID = fmt.Sprintf("kimi-1/pair-%d", i)
			in.EffortFormat, in.EffortIn = tc.format, tc.in
			in.Efforts = json.RawMessage(tc.efforts)
			_, err := repo.Create(ctx, in)
			if tc.wantRejected && apperr.CodeOf(err) != apperr.InvalidRequest {
				t.Fatalf("应拒 InvalidRequest, got %v", err)
			}
			if !tc.wantRejected && err != nil {
				t.Fatalf("应放行, got %v", err)
			}
		})
	}
}

// 改名走主键改写:旧 id 失效、新 id 可读;同名 NewID 退化为普通更新;
// 撞已有 id 报 AlreadyExists。
func TestUpdateRename(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatalf("create: %v", err)
	}

	in := input()
	in.NewID = "kimi-1/k2x"
	renamed, err := repo.Update(ctx, in)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.ID != "kimi-1/k2x" {
		t.Errorf("renamed id = %q, want kimi-1/k2x", renamed.ID)
	}
	if _, err := repo.Get(ctx, "kimi-1/k2"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("old id err = %v, want not_found", err)
	}
	if _, err := repo.Get(ctx, "kimi-1/k2x"); err != nil {
		t.Errorf("new id should resolve: %v", err)
	}

	same := input()
	same.ID = "kimi-1/k2x"
	same.NewID = "kimi-1/k2x"
	if _, err := repo.Update(ctx, same); err != nil {
		t.Fatalf("same-id update: %v", err)
	}

	other := input()
	other.ID = "kimi-1/k3"
	if _, err := repo.Create(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	clash := input()
	clash.ID = "kimi-1/k2x"
	clash.NewID = "kimi-1/k3"
	if _, err := repo.Update(ctx, clash); !apperr.Is(err, apperr.AlreadyExists) {
		t.Errorf("clash code = %q, want already_exists", apperr.CodeOf(err))
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo, _ := fixtures(t)
	in := input()
	in.ID = "ghost/x"
	if _, err := repo.Update(context.Background(), in); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestListFiltersByAccount(t *testing.T) {
	repo, accounts := fixtures(t)
	ctx := context.Background()
	if _, err := accounts.Create(ctx, account.Input{
		Name:       "ds-1",
		ProviderID: "deepseek.global.api.standard",
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-ds-secret"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	other := input()
	other.ID = "ds-1/v4"
	other.Account = "ds-1"
	other.NativeModel = "deepseek-v4"
	if _, err := repo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	all, err := repo.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered list = %d, want 2", len(all))
	}

	only, err := repo.List(ctx, "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].ID != "ds-1/v4" {
		t.Errorf("filtered list = %+v", only)
	}
}

func TestListOrderAndReorder(t *testing.T) {
	repo, _ := fixtures(t)
	ctx := context.Background()
	ids := []string{"kimi-1/c", "kimi-1/a", "kimi-1/b"}
	for _, id := range ids {
		in := input()
		in.ID = id
		in.NativeModel = "m-" + id
		if _, err := repo.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	listIDs := func() []string {
		got, err := repo.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(got))
		for _, m := range got {
			out = append(out, m.ID)
		}
		return out
	}
	// 新模型 sort_order 按创建序递增,列表即手动序(创建序),不再 id 序
	if got := listIDs(); !slices.Equal(got, ids) {
		t.Errorf("list = %v, want creation order %v", got, ids)
	}

	want := []string{"kimi-1/b", "kimi-1/c", "kimi-1/a"}
	if err := repo.Reorder(ctx, want); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if got := listIDs(); !slices.Equal(got, want) {
		t.Errorf("list = %v, want %v", got, want)
	}

	// 混入不存在的 id 整批拒,已写的序号随事务回滚
	if err := repo.Reorder(ctx, []string{"kimi-1/a", "ghost/x"}); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
	if got := listIDs(); !slices.Equal(got, want) {
		t.Errorf("failed reorder should not persist, list = %v, want %v", got, want)
	}
}

func TestDeleteKeepsAccount(t *testing.T) {
	repo, accounts := fixtures(t)
	ctx := context.Background()
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "kimi-1/k2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, "kimi-1/k2"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("model should be gone, err = %v", err)
	}
	if _, err := accounts.Get(ctx, "kimi-1"); err != nil {
		t.Errorf("deleting a model must not affect its account: %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	repo, _ := fixtures(t)
	if err := repo.Delete(context.Background(), "ghost/x"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestGetBackfillsCache(t *testing.T) {
	backend := testenv.RedisRequired(t)
	s := testenv.Store(t)
	ctx := context.Background()
	c := cache.New(backend, time.Minute)

	accounts := account.NewRepo(s.Pool(), c)
	if _, err := accounts.Create(ctx, account.Input{
		Name: "kimi-1", ProviderID: "kimi.global.subscribe.coding",
		Credential: credential.Credential{Kind: provider.CredAPIKey, APIKey: "sk-kimi-secret"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(s.Pool(), c, lookupOf(accounts))
	if _, err := repo.Create(ctx, input()); err != nil {
		t.Fatal(err)
	}

	key := cache.ModelKey("kimi-1/k2")
	_ = backend.Del(ctx, key)
	if _, err := repo.Get(ctx, "kimi-1/k2"); err != nil {
		t.Fatal(err)
	}
	if raw, err := backend.Get(ctx, key); err != nil || len(raw) == 0 {
		t.Fatalf("miss should backfill the cache: %v", err)
	}
}
