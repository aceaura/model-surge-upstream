import 'package:flutter/material.dart';

/// CC Switch 式页面进场:内容挂载或 [trigger] 变化时以 0.5s ease-out 淡入
/// (对照 cc-switch 的 `fadeIn 0.5s ease-out`,纯透明度 0→1,不带位移)。
///
/// 只包一层 FadeTransition,子树不随动画重建:IndexedStack 常驻页的状态
/// (已加载数据、滚动位置、搜索词)在切页重播动画时全部保留。
class PageEntrance extends StatefulWidget {
  const PageEntrance({super.key, required this.child, this.trigger});

  final Widget child;

  /// 值变化时重播进场动画(如页签 id);始终为 null 则只在挂载时播一次。
  final Object? trigger;

  @override
  State<PageEntrance> createState() => _PageEntranceState();
}

class _PageEntranceState extends State<PageEntrance>
    with SingleTickerProviderStateMixin {
  late final AnimationController _controller = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 500),
  );
  late final Animation<double> _opacity =
      CurvedAnimation(parent: _controller, curve: Curves.easeOut);

  @override
  void initState() {
    super.initState();
    _controller.forward();
  }

  @override
  void didUpdateWidget(PageEntrance oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.trigger != widget.trigger) {
      _controller.forward(from: 0);
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) =>
      FadeTransition(opacity: _opacity, child: widget.child);
}
