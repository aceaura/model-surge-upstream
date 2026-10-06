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

/// 一个厂商(displayName)下的全部类型条目,按 订阅在前、区域名 排序。
class _VendorGroup {
  _VendorGroup(this.name, this.specs);

  final String name;
  final List<ProviderSpec> specs;
}

class _ProvidersPageState extends State<ProvidersPage> {
  late Future<List<ProviderSpec>> _future = widget.client.listProviders();
  String _query = '';

  void _reload() =>
      setState(() => _future = widget.client.listProviders());

  /// 按厂商分组:同 displayName 的条目并入一组,组内订阅在前;
  /// 搜索命中厂商名或任一条目 id 即保留整组。
  List<_VendorGroup> _groupsOf(List<ProviderSpec> providers) {
    final q = _query.toLowerCase();
    final byName = <String, List<ProviderSpec>>{};
    for (final p in providers) {
      byName.putIfAbsent(p.displayName, () => []).add(p);
    }
    final groups = [
      for (final e in byName.entries)
        _VendorGroup(e.key, List.of(e.value)
          ..sort((a, b) {
            final sub = (a.billing == 'subscription' ? 0 : 1)
                .compareTo(b.billing == 'subscription' ? 0 : 1);
            if (sub != 0) return sub;
            final region = a.region.compareTo(b.region);
            return region != 0 ? region : a.plan.compareTo(b.plan);
          })),
    ];
    if (q.isEmpty) return groups;
    return groups
        .where((g) =>
            g.name.toLowerCase().contains(q) ||
            g.specs.any((p) => p.id.toLowerCase().contains(q)))
        .toList();
  }

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
        // 摘要带:按协议统计覆盖的提供商数;搜索按厂商名/条目 id 过滤
        final protoCounts = <String, int>{};
        for (final p in providers) {
          for (final proto in p.protocols) {
            protoCounts[proto] = (protoCounts[proto] ?? 0) + 1;
          }
        }
        final visible = _groupsOf(providers);
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
                                                ? _VendorCard(
                                                    group: visible[j])
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

/// 厂商大卡:卡头头像+厂商名,右上角一枚厂商小标;卡内按类型分节(节首类型签)。
class _VendorCard extends StatelessWidget {
  const _VendorCard({required this.group});

  final _VendorGroup group;

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
                ProviderAvatar(providerId: group.specs.first.id, size: 36),
                const SizedBox(width: 12),
                Text(
                  group.name,
                  style: TextStyle(
                    fontSize: 15,
                    fontWeight: FontWeight.w600,
                    color: t.ink,
                  ),
                ),
                const Spacer(),
                ProviderTag(providerVendor(group.specs.first.id)),
              ],
            ),
            for (var i = 0; i < group.specs.length; i++) ...[
              if (i > 0)
                Divider(height: 26, thickness: 1, color: t.border)
              else
                const SizedBox(height: 12),
              Row(
                children: [
                  _TypeChip(spec: group.specs[i]),
                ],
              ),
              const SizedBox(height: 8),
              // 服务类型行只在有真实类型时渲染:Standard 是单一服务类型的
              // 占位标签,界面隐藏。
              if (group.specs[i].hasServiceType)
                _row(context, '服务类型', group.specs[i].plan),
              _row(context, '官网', group.specs[i].website),
              _row(context, '请求地址', group.specs[i].baseUrl),
              _row(context, '支持协议', group.specs[i].protocols.join(', ')),
              _row(context, '认证形态', group.specs[i].auth),
              _row(context, '凭据形态',
                  group.specs[i].credential == 'kiro_refresh'
                      ? 'Kiro 登录态 (Desktop / SSO)'
                      : group.specs[i].credential),
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

/// 类型签「计费模式 · 服务区域」:订阅紫、按量计费橙。
class _TypeChip extends StatelessWidget {
  const _TypeChip({required this.spec});

  final ProviderSpec spec;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final subscription = spec.billing == 'subscription';
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: subscription
            ? t.violet.withValues(alpha: .10)
            : t.warnSoft,
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        spec.typeLabel,
        style: TextStyle(
          fontSize: 11.5,
          fontWeight: FontWeight.w500,
          color: subscription ? t.violet : t.warnInk,
        ),
      ),
    );
  }
}
