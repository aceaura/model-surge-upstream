import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import 'account_form.dart';
import 'models_page.dart';
import 'quota_dialog.dart';

class AccountsPage extends StatefulWidget {
  const AccountsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

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

  void _openModels(Account account, List<ProviderSpec> providers) {
    Navigator.of(context).push(MaterialPageRoute(
      builder: (_) => ModelsPage(
        client: widget.client,
        account: account,
        providers: providers,
        onOpenSettings: widget.onOpenSettings,
      ),
    ));
  }

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
        return Column(
          children: [
            Padding(
              padding: const EdgeInsets.all(16),
              child: Row(
                children: [
                  Text('账号 ${accounts.length} 个',
                      style: Theme.of(context).textTheme.titleMedium),
                  const Spacer(),
                  IconButton(
                    tooltip: '刷新',
                    icon: const Icon(Icons.refresh),
                    onPressed: _reload,
                  ),
                  const SizedBox(width: 8),
                  FilledButton.icon(
                    onPressed: () => _create(providers),
                    icon: const Icon(Icons.add),
                    label: const Text('新建账号'),
                  ),
                ],
              ),
            ),
            const Divider(height: 1),
            Expanded(
              child: accounts.isEmpty
                  ? const Center(child: Text('还没有账号，先新建一个。'))
                  : ListView.separated(
                      padding: const EdgeInsets.all(16),
                      itemCount: accounts.length,
                      separatorBuilder: (_, _) => const SizedBox(height: 8),
                      itemBuilder: (context, i) {
                        final a = accounts[i];
                        return Card(
                          child: ListTile(
                            title: Row(
                              children: [
                                Text(a.name),
                                const SizedBox(width: 8),
                                Chip(label: Text(a.providerId)),
                              ],
                            ),
                            subtitle: Text([
                              '密钥 ${a.maskedApiKey}',
                              if (a.baseUrl.isNotEmpty) '地址 ${a.baseUrl}',
                              if (a.headers.isNotEmpty)
                                '自定义头 ${a.headers.length} 个',
                            ].join('   ')),
                            trailing: Row(
                              mainAxisSize: MainAxisSize.min,
                              children: [
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
                                IconButton(
                                  tooltip: '模型',
                                  icon: const Icon(Icons.list_alt),
                                  onPressed: () => _openModels(a, providers),
                                ),
                                IconButton(
                                  tooltip: '额度',
                                  icon: const Icon(Icons.savings_outlined),
                                  onPressed: () => _showQuota(a),
                                ),
                                IconButton(
                                  tooltip: '编辑',
                                  icon: const Icon(Icons.edit_outlined),
                                  onPressed: () => _edit(a, providers),
                                ),
                                IconButton(
                                  tooltip: '删除',
                                  icon: const Icon(Icons.delete_outline),
                                  onPressed: () => _delete(a),
                                ),
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
