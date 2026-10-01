import 'package:flutter/material.dart';

import '../theme.dart';

/// 顶部滑出提示框(仿 fengshen-slicer TopToast):浅色卡片从顶部中间划入,
/// 2.4s 后自动滑出,不拦截点击。成功/失败共用同一骨架,仅图标与图标色不同,
/// 取代 Material 底部黑色 SnackBar。
class TopToast {
  static void show(BuildContext context, String msg, {bool error = false}) {
    final overlay = Overlay.maybeOf(context);
    if (overlay == null) return;
    late OverlayEntry entry;
    entry = OverlayEntry(
      builder: (_) => _TopToast(msg: msg, error: error, onDone: entry.remove),
    );
    overlay.insert(entry);
  }
}

class _TopToast extends StatefulWidget {
  final String msg;
  final bool error;
  final VoidCallback onDone;
  const _TopToast(
      {required this.msg, required this.error, required this.onDone});

  @override
  State<_TopToast> createState() => _TopToastState();
}

class _TopToastState extends State<_TopToast>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c;
  late final Animation<Offset> _slide;

  @override
  void initState() {
    super.initState();
    _c = AnimationController(
        vsync: this, duration: const Duration(milliseconds: 220));
    _slide = Tween(begin: const Offset(0, -1.4), end: Offset.zero)
        .animate(CurvedAnimation(parent: _c, curve: Curves.easeOutCubic));
    _c.forward();
    Future.delayed(const Duration(milliseconds: 2400), () async {
      if (!mounted) return;
      await _c.reverse();
      if (mounted) widget.onDone();
    });
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final color = widget.error ? t.danger : t.success;
    return Positioned(
      top: 58,
      left: 0,
      right: 0,
      child: IgnorePointer(
        // Overlay 里包一层透明 Material:否则 Text 拿不到 Material 兜底样式,
        // 会带黄色下划线。
        child: Material(
          color: Colors.transparent,
          child: Align(
            alignment: Alignment.topCenter,
            child: SlideTransition(
              position: _slide,
              child: FadeTransition(
                opacity: _c,
                child: Container(
                  constraints: const BoxConstraints(maxWidth: 520),
                  padding: const EdgeInsets.symmetric(
                      horizontal: 16, vertical: 12),
                  decoration: BoxDecoration(
                    color: t.surface,
                    border: Border.all(color: t.border),
                    borderRadius: BorderRadius.circular(10),
                    boxShadow: const [
                      BoxShadow(
                          color: Color(0x1A161C28),
                          blurRadius: 16,
                          offset: Offset(0, 6)),
                    ],
                  ),
                  child: Row(mainAxisSize: MainAxisSize.min, children: [
                    Icon(
                      widget.error ? Icons.error_outline : Icons.check_circle,
                      size: 17,
                      color: color,
                    ),
                    const SizedBox(width: 9),
                    Flexible(
                      child: _ToastText(msg: widget.msg, ink: t.ink),
                    ),
                  ]),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// 提示文字:含换行/分号分隔的多条内容时按行展开(多排文字框)。
class _ToastText extends StatelessWidget {
  final String msg;
  final Color ink;
  const _ToastText({required this.msg, required this.ink});

  @override
  Widget build(BuildContext context) {
    final rows = msg
        .split(RegExp(r'[\n；;]'))
        .map((s) => s.trim())
        .where((s) => s.isNotEmpty)
        .toList();
    final style = TextStyle(fontSize: 12.5, color: ink, height: 1.6);
    if (rows.length <= 1) {
      return Text(msg, style: style);
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [for (final r in rows) Text(r, style: style)],
    );
  }
}
