import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/feedback.dart';
import '../ui/hover_card.dart';
import '../ui/provider_avatar.dart';
import 'account_form.dart';
import 'quota_dialog.dart';

class AccountsPage extends StatefulWidget {
  const AccountsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
    required this.onOpenModels,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  /// 点「模型」时通知主壳切换到内嵌模型页(不再推路由)。
  final void Function(Account account, List<ProviderSpec> providers)
      onOpenModels;

  @override
  State<AccountsPage> createState() => _AccountsPageState();
}

class _AccountsPageState extends State<AccountsPage> {
  late Future<(List<Account>, List<ProviderSpec>)> _future = _load();
  final _toggling = <String>{};

  Future<(List<Account>, List<ProviderSpec>)> _load() async {
    final accounts = await widget.client.listAccounts();
    final providers = await widget.client.listProviders();
    return (accounts, providers);
  }

  void _reload() => setState(() => _future = _load());

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

  Future<void> _create(List<ProviderSpec> providers) async {
    final saved = await showDialog<bool>(
      context: context,
      builder: (_) => AccountForm(client: widget.client, providers: providers),
    );
    if (saved == true) _reload();
  }

  Future<void> _edit(Account account, List<ProviderSpec> providers) async {
    final saved = await showDialog<bool>(
      context: context,
      builder: (_) => AccountForm(
        client: widget.client,
        providers: providers,
        editing: account,
      ),
    );
    if (saved == true) _reload();
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
      builder: (context) => AlertDialog(
        title: Text('删除账号 ${account.name}？'),
        content: Text(modelCount == 0
            ? '该账号下没有模型，删除后不可恢复。'
            : '该操作将级联删除其下全部 $modelCount 个模型，删除后不可恢复。'),
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
      final deleted = await widget.client.deleteAccount(account.name);
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text('已删除账号 ${account.name}，级联删除 ${deleted.length} 个模型'),
      ));
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  void _openModels(Account account, List<ProviderSpec> providers) =>
      widget.onOpenModels(account, providers);

  Future<void> _showQuota(Account account) => showDialog<void>(
        context: context,
        builder: (_) => QuotaDialog(client: widget.client, account: account),
      );

  @override
  Widget build(BuildContext context) {
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
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(24, 20, 24, 14),
              child: Row(
                children: [
                  Text(
                    '账号',
                    style: TextStyle(
                      fontSize: 21,
                      fontWeight: FontWeight.w700,
                      color: t.ink,
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text('${accounts.length} 个',
                      style: TextStyle(fontSize: 12.5, color: t.faint)),
                  const Spacer(),
                  OutlinedButton.icon(
                    onPressed: _reload,
                    icon: const Icon(Icons.refresh, size: 15),
                    label: const Text('刷新'),
                  ),
                  const SizedBox(width: 10),
                  FilledButton.icon(
                    onPressed: () => _create(providers),
                    icon: const Icon(Icons.add, size: 16),
                    label: const Text('新建账号'),
                  ),
                ],
              ),
            ),
            Expanded(
              child: accounts.isEmpty
                  ? Center(
                      child: Text('还没有账号，先新建一个。',
                          style: TextStyle(color: t.faint)))
                  : ListView.separated(
                      padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
                      itemCount: accounts.length,
                      separatorBuilder: (_, _) => const SizedBox(height: 12),
                      itemBuilder: (context, i) {
                        final a = accounts[i];
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
                                          Chip(
                                            label: Text(a.providerId),
                                            visualDensity:
                                                VisualDensity.compact,
                                          ),
                                        ],
                                      ),
                                      const SizedBox(height: 4),
                                      Text(
                                        [
                                          '密钥 ${a.maskedApiKey}',
                                          if (a.baseUrl.isNotEmpty)
                                            '地址 ${a.baseUrl}',
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
                                _action(Icons.list_alt, '模型',
                                    () => _openModels(a, providers), t),
                                _action(Icons.savings_outlined, '额度',
                                    () => _showQuota(a), t),
                                _action(Icons.edit_outlined, '编辑',
                                    () => _edit(a, providers), t),
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
