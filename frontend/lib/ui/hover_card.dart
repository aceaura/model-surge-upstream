import 'package:flutter/material.dart';

import '../theme.dart';

/// 悬停自动亮起的列表行(参照 CC Switch):静止时完全扁平——透明底、
/// 无描边无投影,行间靠细分隔线区分;鼠标进入后行底向 primarySoft
/// 融合成一块圆角浅色高亮,130ms 过渡。仅外壳变化,内部布局不变。
class HoverCard extends StatefulWidget {
  final Widget child;
  const HoverCard({super.key, required this.child});

  @override
  State<HoverCard> createState() => _HoverCardState();
}

class _HoverCardState extends State<HoverCard> {
  bool _hover = false;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final dark = Theme.of(context).brightness == Brightness.dark;
    // 与所在内容区底色一致的"静止色":亮模式纯白,暗模式 bg
    final base = dark ? t.bg : Colors.white;
    final bg = _hover
        ? Color.alphaBlend(t.primarySoft.withValues(alpha: .5), base)
        : base;
    return MouseRegion(
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 130),
        curve: Curves.easeOut,
        decoration: BoxDecoration(
          color: bg,
          borderRadius: BorderRadius.circular(12),
        ),
        child: widget.child,
      ),
    );
  }
}
