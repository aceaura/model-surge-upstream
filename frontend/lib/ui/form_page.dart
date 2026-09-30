import 'package:flutter/material.dart';

import '../theme.dart';
import 'feedback.dart';

/// CC Switch 式整页表单骨架:顶部返回钮+粗标题,中部一整幅白卡
/// (居中身份头像 + 全宽字段分区),底部右对齐动作条(取消/提交)。
/// 新建与编辑都推成独立路由占满窗口,不再用居中弹窗——字段全宽铺开后
/// 长地址、JSON 参数不再挤在窄对话框里。
class FormPage extends StatelessWidget {
  const FormPage({
    super.key,
    required this.title,
    required this.child,
    required this.onCancel,
    required this.onSubmit,
    required this.submitLabel,
    this.submitEnabled = true,
    this.busy = false,
    this.avatar,
  });

  final String title;

  /// 白卡内的表单主体。
  final Widget child;

  /// 返回钮与取消钮共用:放弃修改离开。
  final VoidCallback onCancel;

  final VoidCallback? onSubmit;
  final String submitLabel;
  final bool submitEnabled;
  final bool busy;

  /// 卡顶居中的身份图标(如提供商头像),null 时不渲染。
  final Widget? avatar;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Scaffold(
      backgroundColor: t.bg,
      body: SafeArea(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 16, 24, 10),
              child: Row(
                children: [
                  _BackButton(onTap: onCancel),
                  const SizedBox(width: 12),
                  Text(
                    title,
                    style: TextStyle(
                      fontSize: 16,
                      fontWeight: FontWeight.w700,
                      color: t.ink,
                    ),
                  ),
                ],
              ),
            ),
            Expanded(
              child: SingleChildScrollView(
                padding: const EdgeInsets.fromLTRB(24, 4, 24, 20),
                child: Container(
                  decoration: BoxDecoration(
                    color: t.surface,
                    border: Border.all(color: t.border),
                    borderRadius: BorderRadius.circular(AppConst.radiusCard),
                  ),
                  padding: const EdgeInsets.fromLTRB(28, 26, 28, 26),
                  child: child,
                ),
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(24, 0, 24, 14),
              child: Row(
                children: [
                  const Spacer(),
                  TextButton(
                    onPressed: busy ? null : onCancel,
                    child: const Text('取消'),
                  ),
                  const SizedBox(width: 10),
                  BusyButton(
                    busy: busy,
                    onPressed: submitEnabled ? onSubmit : null,
                    child: Text(submitLabel),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// 顶栏返回钮:CC Switch 式圆角方框描边 + 左箭头。
class _BackButton extends StatelessWidget {
  const _BackButton({required this.onTap});

  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return InkWell(
      borderRadius: BorderRadius.circular(10),
      onTap: onTap,
      child: Container(
        width: 34,
        height: 34,
        decoration: BoxDecoration(
          border: Border.all(color: t.border),
          borderRadius: BorderRadius.circular(10),
        ),
        child: Icon(Icons.arrow_back, size: 17, color: t.dim),
      ),
    );
  }
}

/// label 在框上的字段(CC Switch 式):小号深色 label + 下方全宽控件。
class LabeledField extends StatelessWidget {
  const LabeledField({super.key, required this.label, required this.child});

  final String label;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(bottom: 6),
          child: Text(
            label,
            style: TextStyle(fontSize: 12.5, fontWeight: FontWeight.w600, color: t.dim),
          ),
        ),
        child,
      ],
    );
  }
}

/// 双列字段行(CC Switch 式名称/备注排法):两列等宽,列间 18。
class FormRow2 extends StatelessWidget {
  const FormRow2(this.left, this.right, {super.key});

  final Widget left;
  final Widget right;

  @override
  Widget build(BuildContext context) => Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(child: left),
          const SizedBox(width: 18),
          Expanded(child: right),
        ],
      );
}

/// 表单内分区标题(CC Switch 式粗标题 + 灰说明),控件跟在下方。
class FormSection extends StatelessWidget {
  const FormSection({
    super.key,
    required this.title,
    this.desc,
    this.trailing,
    required this.child,
  });

  final String title;
  final String? desc;

  /// 标题行右侧控件(如「添加」钮)。
  final Widget? trailing;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Text(
              title,
              style: TextStyle(fontSize: 13.5, fontWeight: FontWeight.w600, color: t.ink),
            ),
            const Spacer(),
            if (trailing != null) ?trailing,
          ],
        ),
        if (desc != null) ...[
          const SizedBox(height: 3),
          Text(desc!, style: TextStyle(fontSize: 12, color: t.faint)),
        ],
        const SizedBox(height: 10),
        child,
      ],
    );
  }
}
