import 'package:flutter/material.dart';

import '../theme.dart';
import 'provider_avatar.dart';

/// 摘要带的一项分类计数。colorKey 决定配色,与头像同一哈希色板,
/// 同一 key(如 deepseek)在头像和 chip 上颜色一致。
class BandStat {
  const BandStat({
    required this.label,
    required this.count,
    required this.colorKey,
  });

  final String label;
  final int count;
  final String colorKey;
}

/// 页头与列表之间的摘要带(CC Switch MCP 页式):统计 pill +
/// 分类计数彩色 chips + 搜索框,白卡承载,是页面的装饰/信息空间。
class SummaryBand extends StatefulWidget {
  const SummaryBand({
    super.key,
    required this.summary,
    required this.stats,
    required this.searchHint,
    required this.onSearch,
    this.controller,
  });

  final String summary;
  final List<BandStat> stats;
  final String searchHint;

  /// 搜索词变化回调(已 trim,可能为空串)。
  final ValueChanged<String> onSearch;

  /// 可选外部搜索框控制器:页面需要程序化改写搜索词时传入
  /// (如从账号页跳转过来并预填账号名);生命周期由外部负责。
  final TextEditingController? controller;

  @override
  State<SummaryBand> createState() => _SummaryBandState();
}

class _SummaryBandState extends State<SummaryBand> {
  late final TextEditingController _controller =
      widget.controller ?? TextEditingController();

  @override
  void dispose() {
    if (widget.controller == null) _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final dark = Theme.of(context).brightness == Brightness.dark;
    final searchBorder = OutlineInputBorder(
      borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
      borderSide: BorderSide.none,
    );
    return Container(
      margin: const EdgeInsets.fromLTRB(24, 2, 24, 12),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
      decoration: BoxDecoration(
        color: t.surface,
        border: Border.all(color: t.border),
        borderRadius: BorderRadius.circular(AppConst.radiusCard),
      ),
      child: Column(
        children: [
          Row(
            children: [
              Container(
                padding:
                    const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                decoration: BoxDecoration(
                  color: t.primarySoft,
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Text(
                  widget.summary,
                  style: TextStyle(
                    fontSize: 12.5,
                    fontWeight: FontWeight.w600,
                    color: t.primaryInk,
                  ),
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Wrap(
                  spacing: 8,
                  runSpacing: 6,
                  children: [
                    for (final s in widget.stats) _chip(s, dark),
                  ],
                ),
              ),
            ],
          ),
          const SizedBox(height: 10),
          TextField(
            controller: _controller,
            onChanged: (v) => widget.onSearch(v.trim()),
            style: const TextStyle(fontSize: 13),
            decoration: InputDecoration(
              isDense: true,
              hintText: widget.searchHint,
              hintStyle: TextStyle(fontSize: 12.5, color: t.faint),
              prefixIcon: Icon(Icons.search, size: 17, color: t.faint),
              prefixIconConstraints:
                  const BoxConstraints(minWidth: 34, minHeight: 30),
              filled: true,
              fillColor: t.bg,
              contentPadding:
                  const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
              border: searchBorder,
              enabledBorder: searchBorder,
              focusedBorder: OutlineInputBorder(
                borderRadius: BorderRadius.circular(AppConst.radiusCtrl),
                borderSide: BorderSide(color: t.primary, width: 1.5),
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _chip(BandStat s, bool dark) {
    final (bg, fg) = ProviderAvatar.colorsFor(s.colorKey, dark: dark);
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        '${s.label}: ${s.count}',
        style: TextStyle(
          fontSize: 11.5,
          fontWeight: FontWeight.w500,
          color: fg,
        ),
      ),
    );
  }
}
