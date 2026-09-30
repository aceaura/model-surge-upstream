import 'dart:math';

import 'package:flutter/material.dart';

import '../theme.dart';

/// CC Switch 式可折叠分栏:整行可点,点击带动画展开/收起内容区。
/// 内容 child 全程留在树里(Align heightFactor 裁剪而非卸载),
/// 折叠不会丢掉分栏内表单已填的状态。
class CollapsibleSection extends StatefulWidget {
  const CollapsibleSection({
    super.key,
    required this.icon,
    required this.title,
    required this.subtitle,
    required this.child,
    this.initiallyExpanded = false,
  });

  final IconData icon;
  final String title;

  /// 收起时也能看到的一句话状态/说明。
  final String subtitle;
  final Widget child;
  final bool initiallyExpanded;

  @override
  State<CollapsibleSection> createState() => _CollapsibleSectionState();
}

class _CollapsibleSectionState extends State<CollapsibleSection>
    with SingleTickerProviderStateMixin {
  late final AnimationController _ctrl = AnimationController(
    duration: const Duration(milliseconds: 220),
    vsync: this,
    value: widget.initiallyExpanded ? 1 : 0,
  );

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  void _toggle() {
    if (_ctrl.isDismissed) {
      _ctrl.forward();
    } else {
      _ctrl.reverse();
    }
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Card(
      margin: EdgeInsets.zero,
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        mainAxisSize: MainAxisSize.min,
        children: [
          InkWell(
            onTap: _toggle,
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 22, vertical: 16),
              child: Row(
                children: [
                  Icon(widget.icon, size: 18, color: t.primaryInk),
                  const SizedBox(width: 12),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          widget.title,
                          style: TextStyle(
                            fontSize: 14,
                            fontWeight: FontWeight.w600,
                            color: t.ink,
                          ),
                        ),
                        const SizedBox(height: 2),
                        Text(
                          widget.subtitle,
                          style: TextStyle(fontSize: 12, color: t.faint),
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(width: 12),
                  AnimatedBuilder(
                    animation: _ctrl,
                    builder: (context, _) => Transform.rotate(
                      angle: _ctrl.value * pi,
                      child: Icon(Icons.expand_more, size: 20, color: t.faint),
                    ),
                  ),
                ],
              ),
            ),
          ),
          AnimatedBuilder(
            animation: _ctrl,
            builder: (context, child) => ClipRect(
              child: Align(
                alignment: Alignment.topCenter,
                heightFactor: _ctrl.value,
                child: child,
              ),
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              mainAxisSize: MainAxisSize.min,
              children: [
                Divider(height: 1, thickness: 1, color: t.border),
                Padding(padding: const EdgeInsets.all(22), child: widget.child),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
