import 'package:flutter/material.dart';

import '../theme.dart';

/// 页面顶部条:主色小竖条 + 16 半粗标题 + 弱化计数,右侧放操作按钮。
/// 取代原先 21 号粗黑大标题(与侧栏选中项语义重复且生硬)。
/// 竖条呼应侧栏「管理/系统」分组标签的绿色小竖条。
class PageHeader extends StatelessWidget {
  const PageHeader({
    super.key,
    required this.title,
    this.count,
    this.leading,
    required this.trailing,
  });

  final String title;

  /// 条目计数,为 null 时不显示(数据未加载完)。
  final int? count;

  /// 标题前的额外控件(如模型页的返回按钮)。
  final Widget? leading;

  /// 右侧操作区,间距由调用方在各按钮间自理。
  final List<Widget> trailing;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Padding(
      padding: const EdgeInsets.fromLTRB(24, 18, 24, 12),
      child: Row(
        children: [
          if (leading != null) ...[leading!, const SizedBox(width: 12)],
          Container(
            width: 4,
            height: 16,
            decoration: BoxDecoration(
              color: t.primary,
              borderRadius: BorderRadius.circular(2),
            ),
          ),
          const SizedBox(width: 8),
          Text(
            title,
            style: TextStyle(
              fontSize: 16,
              fontWeight: FontWeight.w600,
              color: t.ink,
            ),
          ),
          if (count != null) ...[
            const SizedBox(width: 8),
            Text('$count 个', style: TextStyle(fontSize: 12.5, color: t.faint)),
          ],
          const Spacer(),
          ...trailing,
        ],
      ),
    );
  }
}
