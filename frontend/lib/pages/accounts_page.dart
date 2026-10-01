import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/confirm_dialog.dart';
import '../ui/feedback.dart';
import '../ui/hover_card.dart';
import '../ui/page_header.dart';
import '../ui/provider_avatar.dart';
import '../ui/provider_tag.dart';
import '../ui/quota_inline.dart';
import '../ui/summary_band.dart';
import '../ui/top_toast.dart';
import 'account_form.dart';

class AccountsPage extends StatefulWidget {
  const AccountsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
    required this.onOpenModels,
    this.active = false,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  /// 是否正处于前台(由主壳按当前导航传入)。页在 IndexedStack 里常驻,
  /// 别处的改动(改额度脚本/删模型等)不会触发本页重建,重新激活时刷新。
  final bool active;

  /// 点「模型」时通知主壳跳到模型总览页,并以该账号名作为搜索词过滤。
  final void Function(Account account) onOpenModels;

  @override
  State<AccountsPage> createState() => _AccountsPageState();
}

class _AccountsPageState extends State<AccountsPage> {
  late Future<(List<Account>, List<ProviderSpec>)> _future = _load();
  final _toggling = <String>{};
  final _testing = <String>{};
  String _query = '';

  /// 非空时页内内联显示整页表单(替代列表),侧边栏保持可见。
  AccountForm? _form;

  @override
  void didUpdateWidget(AccountsPage oldWidget) {
    super.didUpdateWidget(oldWidget);
    // 重新激活(false→true)时刷新:别处的改动(改额度脚本/API 灌数据等)
    // 不会触发常驻页重建,不刷新会看不到后配的额度脚本与新账号。
    if (!oldWidget.active && widget.active) _reload();
  }

  Future<(List<Account>, List<ProviderSpec>)> _load() async {
    final accounts = await widget.client.listAccounts();
    final providers = await widget.client.listProviders();
    return (accounts, providers);
  }

  // 块体而非 => 箭头:setState 断言回调不得返回 Future,箭头写法会把
  // 赋值表达式的 Future 带回去,debug 下每次 _reload 都触发断言。
  void _reload() => setState(() {
        _future = _load();
      });

  /// 卡片尾部的小号操作按钮:18 图标 + 弱色,hover 才有底色反馈。
  Widget _action(
      IconData icon, String tooltip, VoidCallback onPressed, AppTokens t) {
    return IconButton(
      tooltip: tooltip,
      icon: Icon(icon, size: 18, color: t.faint),
      splashRadius: 18,
      visualDensity: VisualDensity.compact,
      onPressed: onPressed,
    );
  }

  /// 整页表单(CC Switch 式):页内内联替换列表,不再推根路由遮盖侧边栏。
  void _openForm(AccountForm form) => setState(() => _form = form);

  /// 表单收尾:关闭内联表单,已保存则重载列表。
  void _closeForm(bool saved) {
    setState(() => _form = null);
    if (saved) _reload();
  }

  void _create(List<ProviderSpec> providers) {
    _openForm(AccountForm(
      client: widget.client,
      providers: providers,
      onDone: _closeForm,
    ));
  }

  void _edit(Account account, List<ProviderSpec> providers) {
    _openForm(AccountForm(
      client: widget.client,
      providers: providers,
      onDone: _closeForm,
      editing: account,
    ));
  }

  /// 拷贝(CC Switch 式):以该账号配置预填新建整页表单,密钥需重填。
  void _copy(Account account, List<ProviderSpec> providers) {
    _openForm(AccountForm(
      client: widget.client,
      providers: providers,
      onDone: _closeForm,
      copyFrom: account,
    ));
  }

  Future<void> _toggle(Account account) async {
    setState(() => _toggling.add(account.name));
    try {
      await widget.client
          .updateAccount(name: account.name, enabled: !account.enabled);
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _toggling.remove(account.name));
    }
  }

  Future<void> _delete(Account account) async {
    int modelCount;
    try {
      (_, modelCount) = await widget.client.getAccount(account.name);
    } catch (e) {
      if (mounted) showError(context, e);
      return;
    }
    if (!mounted) return;

    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => ConfirmDialog(
        title: '删除账号 ${account.name}？',
        message: modelCount == 0
            ? '该账号下没有模型，删除后不可恢复。'
            : '该操作将级联删除其下全部 $modelCount 个模型，删除后不可恢复。',
        confirmLabel: '确认删除',
      ),
    );
    if (confirmed != true) return;

    try {
      final deleted = await widget.client.deleteAccount(account.name);
      if (!mounted) return;
      TopToast.show(
          context, '已删除账号 ${account.name}，级联删除 ${deleted.length} 个模型');
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  void _openModels(Account account) => widget.onOpenModels(account);

  /// 检测连通性(参考 CC Switch stream_check):GET 生效请求地址,拿到任意
  /// HTTP 响应即「可达」——可达 ≠ 凭据正确,凭据与模型名的验证在模型页
  /// 的模型级检测。结果用顶部提示框呈现,行内容不因此抖动。
  Future<void> _test(Account account) async {
    setState(() => _testing.add(account.name));
    try {
      final res = await widget.client.testAccount(account.name);
      if (!mounted) return;
      if (res.ok) {
        TopToast.show(context,
            '账号 ${account.name} 可达 · HTTP ${res.statusCode} · ${res.latencyMs} ms');
      } else {
        TopToast.show(context, '账号 ${account.name} 检测失败：${res.error}',
            error: true);
      }
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _testing.remove(account.name));
    }
  }

  @override
  Widget build(BuildContext context) {
    // 表单打开时整幅替换列表内容(侧边栏在主壳,不受影响)。
    final form = _form;
    if (form != null) return form;
    return FutureBuilder<(List<Account>, List<ProviderSpec>)>(
      future: _future,
      builder: (context, snapshot) {
        if (snapshot.connectionState != ConnectionState.done) {
          return const Center(child: CircularProgressIndicator());
        }
        if (snapshot.hasError) {
          return ErrorPanel(
            error: snapshot.error!,
            onRetry: _reload,
            onOpenSettings: widget.onOpenSettings,
          );
        }
        final (accounts, providers) = snapshot.data!;
        final t = context.tokens;
        // 摘要带:按提供商统计账号数;搜索按名称/提供商/地址过滤
        final counts = <String, int>{};
        for (final a in accounts) {
          counts[a.providerId] = (counts[a.providerId] ?? 0) + 1;
        }
        final q = _query.toLowerCase();
        final visible = q.isEmpty
            ? accounts
            : accounts
                .where((a) =>
                    a.name.toLowerCase().contains(q) ||
                    a.providerId.toLowerCase().contains(q) ||
                    a.baseUrl.toLowerCase().contains(q))
                .toList();
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            PageHeader(
              title: '账号',
              count: accounts.length,
              // 页头不放刷新:数据随操作自动重载,重试入口在错误面板
              trailing: [
                FilledButton.icon(
                  onPressed: () => _create(providers),
                  icon: const Icon(Icons.add, size: 16),
                  label: const Text('新建账号'),
                ),
              ],
            ),
            SummaryBand(
              summary: '已配置 ${accounts.length} 个账号',
              stats: [
                for (final e in counts.entries)
                  BandStat(label: e.key, count: e.value, colorKey: e.key),
              ],
              searchHint: '搜索账号名、提供商或请求地址',
              onSearch: (v) => setState(() => _query = v),
            ),
            Expanded(
              child: visible.isEmpty
                  ? Center(
                      child: Text(
                          accounts.isEmpty ? '还没有账号，先新建一个。' : '没有匹配的账号。',
                          style: TextStyle(color: t.faint)))
                  : ListView.separated(
                      padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
                      itemCount: visible.length,
                      separatorBuilder: (_, _) => const SizedBox(height: 12),
                      itemBuilder: (context, i) {
                        final a = visible[i];
                        final spec = providers
                            .where((p) => p.id == a.providerId)
                            .firstOrNull;
                        // 副标题显示实际生效的请求地址(覆盖优先,否则提供商
                        // 默认),比脱敏密钥更能区分账号;密钥只在编辑弹窗出现
                        final effectiveUrl =
                            a.baseUrl.isNotEmpty ? a.baseUrl : spec?.baseUrl ?? '';
                        return HoverCard(
                          child: Padding(
                            padding: const EdgeInsets.symmetric(
                                horizontal: 16, vertical: 14),
                            child: Row(
                              children: [
                                ProviderAvatar(providerId: a.providerId),
                                const SizedBox(width: 13),
                                Expanded(
                                  child: Column(
                                    crossAxisAlignment:
                                        CrossAxisAlignment.start,
                                    children: [
                                      Row(
                                        children: [
                                          Flexible(
                                            child: Text(
                                              a.name,
                                              overflow: TextOverflow.ellipsis,
                                              style: TextStyle(
                                                fontSize: 15,
                                                fontWeight: FontWeight.w600,
                                                color: t.ink,
                                              ),
                                            ),
                                          ),
                                          const SizedBox(width: 8),
                                          ProviderTag(a.providerId),
                                        ],
                                      ),
                                      const SizedBox(height: 4),
                                      Text(
                                        [
                                          effectiveUrl,
                                          if (a.headers.isNotEmpty)
                                            '自定义头 ${a.headers.length} 个',
                                        ].join('   '),
                                        overflow: TextOverflow.ellipsis,
                                        style: TextStyle(
                                            fontSize: 12, color: t.faint),
                                      ),
                                    ],
                                  ),
                                ),
                                const SizedBox(width: 8),
                                // 行内额度摘要(CC Switch 式):提供商声明了额度
                                // 接口或账号启用了额度脚本才出现,加载后自动
                                // 查询一次;脚本配了自动间隔则按间隔自刷
                                QuotaInline(
                                  client: widget.client,
                                  accountName: a.name,
                                  queryable: (spec?.quotaQueryable ?? false) ||
                                      (a.quotaScript?.active ?? false),
                                  autoIntervalMinutes:
                                      a.quotaScript?.autoIntervalMinutes ?? 0,
                                ),
                                const SizedBox(width: 8),
                                if (_toggling.contains(a.name))
                                  const SizedBox(
                                    width: 24,
                                    height: 24,
                                    child: CircularProgressIndicator(
                                        strokeWidth: 2),
                                  )
                                else
                                  Switch(
                                    value: a.enabled,
                                    onChanged: (_) => _toggle(a),
                                  ),
                                // 操作顺序仿 CC Switch:编辑、拷贝、检测、
                                // 删除;「模型」是 msu 独有按钮,顺延到第四
                                _action(Icons.edit_outlined, '编辑',
                                    () => _edit(a, providers), t),
                                _action(Icons.copy_outlined, '拷贝',
                                    () => _copy(a, providers), t),
                                if (_testing.contains(a.name))
                                  const Padding(
                                    padding: EdgeInsets.all(12),
                                    child: SizedBox(
                                      width: 18,
                                      height: 18,
                                      child: CircularProgressIndicator(
                                          strokeWidth: 2),
                                    ),
                                  )
                                else
                                  _action(Icons.network_check, '检测连通性',
                                      () => _test(a), t),
                                _action(Icons.list_alt, '模型',
                                    () => _openModels(a), t),
                                _action(Icons.delete_outline, '删除',
                                    () => _delete(a), t),
                              ],
                            ),
                          ),
                        );
                      },
                    ),
            ),
          ],
        );
      },
    );
  }
}
