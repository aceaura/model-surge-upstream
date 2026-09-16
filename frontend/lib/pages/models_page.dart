import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';
import 'model_form.dart';

/// 某账号下的模型列表。只列出归属该账号的模型。
class ModelsPage extends StatefulWidget {
  const ModelsPage({
    super.key,
    required this.client,
    required this.account,
    required this.providers,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final Account account;
  final List<ProviderSpec> providers;
  final VoidCallback onOpenSettings;

  @override
  State<ModelsPage> createState() => _ModelsPageState();
}

class _ModelsPageState extends State<ModelsPage> {
  late Future<(List<UpstreamModel>, List<Account>)> _future = _load();
  final _toggling = <String>{};

  Future<(List<UpstreamModel>, List<Account>)> _load() async {
    final models = await widget.client.listModels(account: widget.account.name);
    final accounts = await widget.client.listAccounts();
    return (models, accounts);
  }

  void _reload() => setState(() => _future = _load());

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

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: Text('${widget.account.name} 的模型'),
        actions: [
          IconButton(
            tooltip: '刷新',
            icon: const Icon(Icons.refresh),
            onPressed: _reload,
          ),
        ],
      ),
      body: FutureBuilder<(List<UpstreamModel>, List<Account>)>(
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
          final (models, accounts) = snapshot.data!;
          return Column(
            children: [
              Padding(
                padding: const EdgeInsets.all(16),
                child: Row(
                  children: [
                    Text('模型 ${models.length} 个',
                        style: Theme.of(context).textTheme.titleMedium),
                    const Spacer(),
                    FilledButton.icon(
                      onPressed: () => _create(accounts),
                      icon: const Icon(Icons.add),
                      label: const Text('新建模型'),
                    ),
                  ],
                ),
              ),
              const Divider(height: 1),
              Expanded(
                child: models.isEmpty
                    ? const Center(child: Text('该账号下还没有模型。'))
                    : ListView.separated(
                        padding: const EdgeInsets.all(16),
                        itemCount: models.length,
                        separatorBuilder: (_, _) => const SizedBox(height: 8),
                        itemBuilder: (context, i) {
                          final m = models[i];
                          return Card(
                            child: ListTile(
                              title: Row(
                                children: [
                                  Text(m.id),
                                  const SizedBox(width: 8),
                                  Chip(label: Text(m.protocol)),
                                ],
                              ),
                              subtitle: Text([
                                '上游 ${m.nativeModel}',
                                '窗口 ${m.contextWindow == 0 ? '未声明' : m.contextWindow}',
                                if (m.defaults.isNotEmpty)
                                  '默认参数 ${m.defaults.length} 项',
                                if (m.overrides.isNotEmpty)
                                  '覆盖参数 ${m.overrides.length} 项',
                              ].join('   ')),
                              trailing: Row(
                                mainAxisSize: MainAxisSize.min,
                                children: [
                                  if (_toggling.contains(m.id))
                                    const SizedBox(
                                      width: 24,
                                      height: 24,
                                      child: CircularProgressIndicator(
                                          strokeWidth: 2),
                                    )
                                  else
                                    Switch(
                                      value: m.enabled,
                                      onChanged: (_) => _toggle(m),
                                    ),
                                  IconButton(
                                    tooltip: '编辑',
                                    icon: const Icon(Icons.edit_outlined),
                                    onPressed: () => _edit(m, accounts),
                                  ),
                                  IconButton(
                                    tooltip: '删除',
                                    icon: const Icon(Icons.delete_outline),
                                    onPressed: () => _delete(m),
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
      ),
    );
  }
}
