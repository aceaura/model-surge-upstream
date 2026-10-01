/// 错误展示与进行中状态的共用件。三类失败在此统一成可操作提示。
library;

import 'package:flutter/material.dart';

import '../api_client.dart';
import 'top_toast.dart';

/// describeError 把异常转成面向运维者的文案。
String describeError(Object error) => switch (error) {
      UnreachableException e => '${e.message}\n请确认服务已启动、地址可达。',
      UnauthorizedException _ => '管理密钥无效，请到设置页更新。',
      ValidationException e => e.message,
      _ => '$error',
    };

/// needsSettings 判断该错误是否应引导运维者去设置页。
bool needsSettings(Object error) =>
    error is UnauthorizedException || error is UnreachableException;

class ErrorPanel extends StatelessWidget {
  const ErrorPanel({
    super.key,
    required this.error,
    this.onRetry,
    this.onOpenSettings,
  });

  final Object error;
  final VoidCallback? onRetry;
  final VoidCallback? onOpenSettings;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 520),
        child: Card(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Icon(Icons.error_outline,
                        color: Theme.of(context).colorScheme.error),
                    const SizedBox(width: 8),
                    const Text('请求失败'),
                  ],
                ),
                const SizedBox(height: 12),
                SelectableText(describeError(error)),
                const SizedBox(height: 16),
                Row(
                  children: [
                    if (onRetry != null)
                      FilledButton(onPressed: onRetry, child: const Text('重试')),
                    if (onRetry != null && onOpenSettings != null)
                      const SizedBox(width: 8),
                    if (onOpenSettings != null && needsSettings(error))
                      OutlinedButton(
                        onPressed: onOpenSettings,
                        child: const Text('打开设置'),
                      ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// showError 用于提交类操作的失败提示（列表已有内容，不该整页替换）。
/// 走顶部滑出提示框,与成功提示同一形态,仅图标/图标色按失败语义区分。
void showError(BuildContext context, Object error) {
  TopToast.show(context, describeError(error), error: true);
}

/// BusyButton 在请求进行中禁用自身并显示进度，避免重复提交。
class BusyButton extends StatelessWidget {
  const BusyButton({
    super.key,
    required this.busy,
    required this.onPressed,
    required this.child,
    this.style,
  });

  final bool busy;
  final VoidCallback? onPressed;
  final Widget child;

  /// 可选外形覆写：默认是常规填充按钮，对话页发送键用它收成圆形图标钮。
  final ButtonStyle? style;

  @override
  Widget build(BuildContext context) {
    return FilledButton(
      style: style,
      onPressed: busy ? null : onPressed,
      child: busy
          ? const SizedBox(
              width: 16,
              height: 16,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : child,
    );
  }
}
