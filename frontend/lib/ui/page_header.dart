import 'package:flutter/material.dart';

import '../theme.dart';

/// 页面顶部条:小标题 + 弱化计数,右侧放操作按钮。
/// 装饰交给页头与列表之间的 SummaryBand,标题本身保持素净。
/// 子页面可传 crumb 形成「父页 › 当前」面包屑,crumb 可点击返回。
class PageHeader extends StatelessWidget {
  const PageHeader({
    super.key,
    required this.title,
    this.count,
    this.leading,
    this.crumb,
    this.onCrumbTap,
    this.avatar,
    required this.trailing,
  });

  final String title;

  /// 条目计数,为 null 时不显示(数据未加载完或摘要带已承载)。
  final int? count;

  /// 标题前的额外控件(如模型页的返回按钮)。
  final Widget? leading;

  /// 父级页名(面包屑前半段),如「账号」;为 null 时不渲染面包屑。
  final String? crumb;

  /// 点击 crumb 的回调(通常与返回按钮同动作)。
  final VoidCallback? onCrumbTap;

  /// 标题前的小号身份图标(如账号的提供商头像)。
  final Widget? avatar;

  /// 右侧操作区,间距由调用方在各按钮间自理。
  final List<Widget> trailing;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final titleText = Text(
      title,
      style: TextStyle(
        fontSize: 16,
        fontWeight: FontWeight.w600,
        color: t.ink,
      ),
    );
    return Padding(
      padding: const EdgeInsets.fromLTRB(24, 18, 24, 12),
      child: Row(
        children: [
          if (leading != null) ...[leading!, const SizedBox(width: 12)],
          if (crumb != null) ...[
            InkWell(
              borderRadius: BorderRadius.circular(6),
              onTap: onCrumbTap,
              child: Padding(
                padding:
                    const EdgeInsets.symmetric(horizontal: 4, vertical: 2),
                child: Text(
                  crumb!,
                  style: TextStyle(fontSize: 13, color: t.faint),
                ),
              ),
            ),
            Icon(Icons.chevron_right, size: 16, color: t.faint),
            const SizedBox(width: 4),
          ],
          if (avatar != null) ...[avatar!, const SizedBox(width: 8)],
          titleText,
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
