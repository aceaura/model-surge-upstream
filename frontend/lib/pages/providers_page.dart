import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../ui/feedback.dart';

/// provider 是编译期常量，界面全部只读。
class ProvidersPage extends StatefulWidget {
  const ProvidersPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  @override
  State<ProvidersPage> createState() => _ProvidersPageState();
}

class _ProvidersPageState extends State<ProvidersPage> {
  late Future<List<ProviderSpec>> _future = widget.client.listProviders();

  void _reload() =>
      setState(() => _future = widget.client.listProviders());

  @override
  Widget build(BuildContext context) {
    return FutureBuilder<List<ProviderSpec>>(
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
        final providers = snapshot.data ?? const <ProviderSpec>[];
        return ListView.separated(
          padding: const EdgeInsets.all(16),
          itemCount: providers.length,
          separatorBuilder: (_, _) => const SizedBox(height: 8),
          itemBuilder: (context, i) => _ProviderCard(spec: providers[i]),
        );
      },
    );
  }
}

class _ProviderCard extends StatelessWidget {
  const _ProviderCard({required this.spec});

  final ProviderSpec spec;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(spec.displayName,
                    style: Theme.of(context).textTheme.titleMedium),
                const SizedBox(width: 8),
                Chip(label: Text(spec.id)),
              ],
            ),
            const SizedBox(height: 12),
            _row('官网', spec.website),
            _row('请求地址', spec.baseUrl),
            _row('支持协议', spec.protocols.join(', ')),
            _row('认证形态', spec.auth),
            _row('凭据形态', spec.credential),
            if (!spec.quotaQueryable)
              _row('额度查询', '不支持')
            else ...[
              _row('额度形态', '${spec.quotaKind} / ${spec.quotaUnit}'),
              _row('额度重置', spec.quotaReset ?? '未声明'),
            ],
          ],
        ),
      ),
    );
  }

  Widget _row(String label, String value) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 2),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(width: 88, child: Text(label)),
            Expanded(child: SelectableText(value)),
          ],
        ),
      );
}
