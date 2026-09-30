import 'package:flutter/material.dart';

import '../theme.dart';

/// 页面顶部条:小标题 + 弱化计数,右侧放操作按钮。
/// 装饰交给页头与列表之间的 SummaryBand,标题本身保持素净。
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
