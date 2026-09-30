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
  String _query = '';

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
        // 摘要带:按协议统计覆盖的提供商数;搜索按 id/显示名过滤
        final protoCounts = <String, int>{};
        for (final p in providers) {
          for (final proto in p.protocols) {
            protoCounts[proto] = (protoCounts[proto] ?? 0) + 1;
          }
        }
        final q = _query.toLowerCase();
        final visible = q.isEmpty
            ? providers
            : providers
                .where((p) =>
                    p.id.toLowerCase().contains(q) ||
                    p.displayName.toLowerCase().contains(q))
                .toList();
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // 页头不放刷新:provider 是编译期常量,刷新无意义
            // (重试入口保留在加载失败时的错误面板上)
            PageHeader(title: '提供商', count: providers.length, trailing: const []),
            SummaryBand(
              summary: '内置 ${providers.length} 家提供商',
              stats: [
                for (final e in protoCounts.entries)
                  BandStat(label: e.key, count: e.value, colorKey: e.key),
              ],
              searchHint: '搜索提供商名称或 id',
              onSearch: (v) => setState(() => _query = v),
            ),
            Expanded(
              // 方块拼接:按可用宽度决定每排个数(非全屏约 2 个,
              // 全屏更多),同排用 IntrinsicHeight 拉齐高度
              child: visible.isEmpty
                  ? Center(
                      child: Text('没有匹配的提供商。',
                          style: TextStyle(color: context.tokens.faint)))
                  : LayoutBuilder(
                      builder: (context, constraints) {
                        final cols = constraints.maxWidth ~/ 430;
                        final perRow = cols < 1 ? 1 : cols;
                        return SingleChildScrollView(
                          padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
                          child: Column(
                            children: [
                              for (var i = 0;
                                  i < visible.length;
                                  i += perRow)
                                Padding(
                                  padding:
                                      const EdgeInsets.only(bottom: 12),
                                  child: IntrinsicHeight(
                                    child: Row(
                                      crossAxisAlignment:
                                          CrossAxisAlignment.stretch,
                                      children: [
                                        for (var j = i;
                                            j < i + perRow;
                                            j++) ...[
                                          if (j > i)
                                            const SizedBox(width: 12),
                                          Expanded(
                                            child: j < visible.length
                                                ? _ProviderCard(
                                                    spec: visible[j])
                                                : const SizedBox(),
                                          ),
                                        ],
                                      ],
                                    ),
                                  ),
                                ),
                            ],
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

class _ProviderCard extends StatelessWidget {
  const _ProviderCard({required this.spec});

  final ProviderSpec spec;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return HoverCard(
      child: Padding(
        padding: const EdgeInsets.all(18),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                ProviderAvatar(providerId: spec.id, size: 36),
                const SizedBox(width: 12),
                Text(
                  spec.displayName,
                  style: TextStyle(
                    fontSize: 15,
                    fontWeight: FontWeight.w600,
                    color: t.ink,
                  ),
                ),
                const SizedBox(width: 8),
                ProviderTag(spec.id),
              ],
            ),
            const SizedBox(height: 12),
            _row(context, '官网', spec.website),
            _row(context, '请求地址', spec.baseUrl),
            _row(context, '支持协议', spec.protocols.join(', ')),
            _row(context, '认证形态', spec.auth),
            _row(context, '凭据形态', spec.credential),
            if (!spec.quotaQueryable)
              _row(context, '额度查询', '不支持')
            else ...[
              if (spec.quotaKind != null)
                _row(context, '额度形态', '${spec.quotaKind} / ${spec.quotaUnit}'),
              _row(context, '额度重置', spec.quotaReset ?? '未声明'),
            ],
          ],
        ),
      ),
    );
  }

  Widget _row(BuildContext context, String label, String value) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 2),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(
              width: 88,
              child: Text(
                label,
                style: TextStyle(fontSize: 12.5, color: context.tokens.faint),
              ),
            ),
            Expanded(
              child: SelectableText(value, style: const TextStyle(fontSize: 13)),
            ),
          ],
        ),
      );
}
