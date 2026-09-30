import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/feedback.dart';
import '../ui/hover_card.dart';
import '../ui/page_header.dart';
import '../ui/provider_avatar.dart';
import '../ui/provider_tag.dart';
import '../ui/summary_band.dart';
import 'model_form.dart';

/// 某账号下的模型列表。内嵌在主壳右侧内容区(不再推路由,侧栏保持可见),
/// 顶部返回按钮回到账号页。
class ModelsPage extends StatefulWidget {
  const ModelsPage({
    super.key,
    required this.client,
    required this.account,
    required this.providers,
    required this.onOpenSettings,
    required this.onBack,
  });

  final ApiClient client;
  final Account account;
  final List<ProviderSpec> providers;
  final VoidCallback onOpenSettings;
  final VoidCallback onBack;

  @override
  State<ModelsPage> createState() => _ModelsPageState();
}

class _ModelsPageState extends State<ModelsPage> {
  late Future<(List<UpstreamModel>, List<Account>)> _future = _load();
  final _toggling = <String>{};
  String _query = '';

  Future<(List<UpstreamModel>, List<Account>)> _load() async {
    final models = await widget.client.listModels(account: widget.account.name);
    final accounts = await widget.client.listAccounts();
    return (models, accounts);
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

  Future<void> _create(List<Account> accounts) async {
    final saved = await showDialog<bool>(
      context: context,
      builder: (_) => ModelForm(
        client: widget.client,
        accounts: accounts,
        providers: widget.providers,
        initialAccount: widget.account.name,
      ),
    );
    if (saved == true) _reload();
  }

  Future<void> _edit(UpstreamModel model, List<Account> accounts) async {
    final saved = await showDialog<bool>(
      context: context,
      builder: (_) => ModelForm(
        client: widget.client,
        accounts: accounts,
        providers: widget.providers,
        initialAccount: widget.account.name,
        editing: model,
      ),
    );
    if (saved == true) _reload();
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
      builder: (context) => AlertDialog(
        title: Text('删除模型 ${model.id}？'),
        content: const Text('删除模型不影响其所属账号。'),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: () => Navigator.of(context).pop(true),
            child: const Text('确认删除'),
          ),
        ],
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
    final t = context.tokens;
    return FutureBuilder<(List<UpstreamModel>, List<Account>)>(
      future: _future,
      builder: (context, snapshot) {
        final loaded =
            snapshot.connectionState == ConnectionState.done && !snapshot.hasError;
        final models = loaded ? snapshot.data!.$1 : const <UpstreamModel>[];
        final accounts = loaded ? snapshot.data!.$2 : const <Account>[];

        // 摘要带:按协议计数 + 搜索过滤(标识/上游模型名)。
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
                    m.nativeModel.toLowerCase().contains(q))
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
              models.isEmpty ? '该账号下还没有模型。' : '没有匹配的模型。',
              style: TextStyle(color: t.faint),
            ),
          );
        } else {
          content = ListView.separated(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            itemCount: visible.length,
            separatorBuilder: (_, _) => const SizedBox(height: 12),
            itemBuilder: (context, i) => _modelCard(visible[i], accounts, t),
          );
        }

        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            PageHeader(
              title: '${widget.account.name} 的模型',
              // 计数交给摘要带「已配置 N 个模型」,页头不再重复。
              leading: _backButton(t),
              crumb: '账号',
              onCrumbTap: widget.onBack,
              avatar: ProviderAvatar(
                providerId: widget.account.providerId,
                size: 22,
              ),
              trailing: [
                OutlinedButton.icon(
                  onPressed: _reload,
                  icon: const Icon(Icons.refresh, size: 15),
                  label: const Text('刷新'),
                ),
                const SizedBox(width: 10),
                FilledButton.icon(
                  onPressed: loaded ? () => _create(accounts) : null,
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
                searchHint: '搜索模型标识或上游模型名',
                onSearch: (v) => setState(() => _query = v),
              ),
            Expanded(child: content),
          ],
        );
      },
    );
  }

  Widget _backButton(AppTokens t) {
    return InkWell(
      borderRadius: BorderRadius.circular(10),
      onTap: widget.onBack,
      child: Container(
        width: 34,
        height: 34,
        decoration: BoxDecoration(
          color: t.surface,
          borderRadius: BorderRadius.circular(10),
          border: Border.all(color: t.border),
        ),
        child: Icon(Icons.arrow_back, size: 17, color: t.dim),
      ),
    );
  }

  Widget _modelCard(UpstreamModel m, List<Account> accounts, AppTokens t) {
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
                      '上游 ${m.nativeModel}',
                      '窗口 ${m.contextWindow == 0 ? '未声明' : m.contextWindow}',
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
            _action(Icons.edit_outlined, '编辑', () => _edit(m, accounts), t),
            _action(Icons.delete_outline, '删除', () => _delete(m), t),
          ],
        ),
      ),
    );
  }
}
