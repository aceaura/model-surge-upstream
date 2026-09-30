import 'package:flutter/material.dart';

import '../theme.dart';

/// 悬停自动亮起的列表卡片(参照 CC Switch):静止时与 cardTheme 完全一致
/// (surface 底 + 16 圆角 + 浅描边 + 极轻投影),鼠标进入后底色向
/// primarySoft 融合、描边转主色,130ms 过渡。仅外壳变化,内部布局不变。
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
    final bg = _hover
        ? Color.alphaBlend(t.primarySoft.withValues(alpha: .5), t.surface)
        : t.surface;
    final border = _hover ? t.primary.withValues(alpha: .45) : t.border;
    return MouseRegion(
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() => _hover = false),
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 130),
        curve: Curves.easeOut,
        decoration: BoxDecoration(
          color: bg,
          borderRadius: BorderRadius.circular(AppConst.radiusCard),
          border: Border.all(color: border),
          boxShadow: dark
              ? null
              : const [
                  BoxShadow(
                    color: Color(0x0A171B24),
                    blurRadius: 3,
                    offset: Offset(0, 1),
                  ),
                ],
        ),
        child: widget.child,
      ),
    );
  }
}
