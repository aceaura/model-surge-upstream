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
import '../ui/summary_band.dart';
import 'model_form.dart';

/// 搜索词种子:主壳在账号页点「模型」时下发,每次点击都是新实例,
/// 保证同一账号连点两次也能触发监听。模型页收到后写入搜索框并按之过滤。
class SearchSeed {
  const SearchSeed(this.text);

  final String text;
}

/// 全部账号下的模型总览。侧栏一级导航页,不按账号分组,
/// 卡片形态与账号内模型页一致,副标题额外标注所属账号以作区分。
class AllModelsPage extends StatefulWidget {
  const AllModelsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
    this.searchSeed,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  /// 外部下发的搜索词种子(账号页「模型」按钮跳转时携带账号名)。
  final ValueNotifier<SearchSeed?>? searchSeed;

  @override
  State<AllModelsPage> createState() => _AllModelsPageState();
}

class _AllModelsPageState extends State<AllModelsPage> {
  late Future<(List<UpstreamModel>, List<Account>, List<ProviderSpec>)>
      _future = _load();
  final _toggling = <String>{};
  final _searchController = TextEditingController();
  String _query = '';

  /// 非空时页内内联显示整页表单(替代列表),侧边栏保持可见。
  ModelForm? _form;

  @override
  void initState() {
    super.initState();
    widget.searchSeed?.addListener(_onSearchSeed);
  }

  @override
  void dispose() {
    widget.searchSeed?.removeListener(_onSearchSeed);
    _searchController.dispose();
    super.dispose();
  }

  /// 种子到达:搜索框填入账号名并立即按之过滤。
  /// 程序化改 controller 不触发 onChanged,这里直接同步 _query。
  void _onSearchSeed() {
    final seed = widget.searchSeed?.value;
    if (seed == null) return;
    _searchController.text = seed.text;
    setState(() => _query = seed.text);
  }

  Future<(List<UpstreamModel>, List<Account>, List<ProviderSpec>)>
      _load() async {
    final models = await widget.client.listModels();
    final accounts = await widget.client.listAccounts();
    final providers = await widget.client.listProviders();
    return (models, accounts, providers);
  }

  void _reload() => setState(() => _future = _load());

  /// 卡片尾部的小号操作按钮:18 图标 + 弱色(与账号页一致)。
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
  void _openForm(ModelForm form) => setState(() => _form = form);

  /// 表单收尾:关闭内联表单,已保存则重载列表。
  void _closeForm(bool saved) {
    setState(() => _form = null);
    if (saved) _reload();
  }

  void _create(List<Account> accounts, List<ProviderSpec> providers,
      String initialAccount) {
    _openForm(ModelForm(
      client: widget.client,
      accounts: accounts,
      providers: providers,
      initialAccount: initialAccount,
      onDone: _closeForm,
    ));
  }

  void _edit(UpstreamModel model, List<Account> accounts,
      List<ProviderSpec> providers) {
    _openForm(ModelForm(
      client: widget.client,
      accounts: accounts,
      providers: providers,
      initialAccount: model.account,
      onDone: _closeForm,
      editing: model,
    ));
  }

  /// 拷贝(CC Switch 式):以该模型配置预填新建整页表单,标识加 -copy 后缀需自行调整。
  void _copy(UpstreamModel model, List<Account> accounts,
      List<ProviderSpec> providers) {
    _openForm(ModelForm(
      client: widget.client,
      accounts: accounts,
      providers: providers,
      initialAccount: model.account,
      onDone: _closeForm,
      copyFrom: model,
    ));
  }

  Future<void> _toggle(UpstreamModel model) async {
    setState(() => _toggling.add(model.id));
    try {
      await widget.client.updateModel(id: model.id, enabled: !model.enabled);
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _toggling.remove(model.id));
    }
  }

  Future<void> _delete(UpstreamModel model) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => ConfirmDialog(
        title: '删除模型 ${model.id}？',
        message: '删除模型不影响其所属账号。',
        confirmLabel: '确认删除',
      ),
    );
    if (confirmed != true) return;
    try {
      await widget.client.deleteModel(model.id);
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// 头像字母取 id 中「账号/」之后的模型名,颜色随名哈希。
  String _shortName(String id) {
    final i = id.indexOf('/');
    return i >= 0 && i + 1 < id.length ? id.substring(i + 1) : id;
  }

  @override
  Widget build(BuildContext context) {
    // 表单打开时整幅替换列表内容(侧边栏在主壳,不受影响)。
    final form = _form;
    if (form != null) return form;
    final t = context.tokens;
    return FutureBuilder<(List<UpstreamModel>, List<Account>, List<ProviderSpec>)>(
      future: _future,
      builder: (context, snapshot) {
        final loaded =
            snapshot.connectionState == ConnectionState.done && !snapshot.hasError;
        final models = loaded ? snapshot.data!.$1 : const <UpstreamModel>[];
        final accounts = loaded ? snapshot.data!.$2 : const <Account>[];
        final providers = loaded ? snapshot.data!.$3 : const <ProviderSpec>[];

        // 摘要带:按协议计数 + 搜索过滤(标识/上游模型名/所属账号)。
        final counts = <String, int>{};
        for (final m in models) {
          counts[m.protocol] = (counts[m.protocol] ?? 0) + 1;
        }
        final q = _query.toLowerCase();
        final visible = q.isEmpty
            ? models
            : models
                .where((m) =>
                    m.id.toLowerCase().contains(q) ||
                    m.nativeModel.toLowerCase().contains(q) ||
                    m.account.toLowerCase().contains(q))
                .toList();

        final Widget content;
        if (snapshot.connectionState != ConnectionState.done) {
          content = const Center(child: CircularProgressIndicator());
        } else if (snapshot.hasError) {
          content = ErrorPanel(
            error: snapshot.error!,
            onRetry: _reload,
            onOpenSettings: widget.onOpenSettings,
          );
        } else if (visible.isEmpty) {
          content = Center(
            child: Text(
              models.isEmpty ? '还没有配置任何模型。' : '没有匹配的模型。',
              style: TextStyle(color: t.faint),
            ),
          );
        } else {
          content = ListView.separated(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            itemCount: visible.length,
            separatorBuilder: (_, _) => const SizedBox(height: 12),
            itemBuilder: (context, i) =>
                _modelCard(visible[i], accounts, providers, t),
          );
        }

        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            PageHeader(
              title: '模型',
              // 页头不放刷新:数据随操作自动重载,重试入口在错误面板
              trailing: [
                FilledButton.icon(
                  onPressed: loaded && accounts.isNotEmpty
                      ? () => _create(accounts, providers, accounts.first.name)
                      : null,
                  icon: const Icon(Icons.add, size: 16),
                  label: const Text('新建模型'),
                ),
              ],
            ),
            if (loaded)
              SummaryBand(
                summary: '已配置 ${models.length} 个模型',
                stats: [
                  for (final e in counts.entries)
                    BandStat(label: e.key, count: e.value, colorKey: e.key),
                ],
                searchHint: '搜索模型标识、上游模型名或账号',
                controller: _searchController,
                onSearch: (v) => setState(() => _query = v),
              ),
            Expanded(child: content),
          ],
        );
      },
    );
  }

  Widget _modelCard(UpstreamModel m, List<Account> accounts,
      List<ProviderSpec> providers, AppTokens t) {
    return HoverCard(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
        child: Row(
          children: [
            ProviderAvatar(providerId: _shortName(m.id)),
            const SizedBox(width: 13),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Flexible(
                        child: Text(
                          m.id,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                            fontSize: 15,
                            fontWeight: FontWeight.w600,
                            color: t.ink,
                          ),
                        ),
                      ),
                      const SizedBox(width: 8),
                      ProviderTag(m.protocol),
                    ],
                  ),
                  const SizedBox(height: 4),
                  Text(
                    [
                      '账号 ${m.account}',
                      '上游 ${m.nativeModel}',
                      '窗口 ${m.contextWindow == 0 ? '未声明' : '${tokensToK(m.contextWindow)}k'}',
                      if (m.defaults.isNotEmpty) '默认参数 ${m.defaults.length} 项',
                      if (m.overrides.isNotEmpty) '覆盖参数 ${m.overrides.length} 项',
                    ].join('   '),
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(fontSize: 12, color: t.faint),
                  ),
                ],
              ),
            ),
            const SizedBox(width: 8),
            if (_toggling.contains(m.id))
              const SizedBox(
                width: 24,
                height: 24,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            else
              Switch(
                value: m.enabled,
                onChanged: (_) => _toggle(m),
              ),
            _action(Icons.edit_outlined, '编辑',
                () => _edit(m, accounts, providers), t),
            _action(Icons.copy_outlined, '拷贝',
                () => _copy(m, accounts, providers), t),
            _action(Icons.delete_outline, '删除', () => _delete(m), t),
          ],
        ),
      ),
    );
  }
}
