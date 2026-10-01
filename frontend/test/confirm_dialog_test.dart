import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:msu_admin/theme.dart';
import 'package:msu_admin/ui/confirm_dialog.dart';
import 'package:msu_admin/ui/dialog_header.dart';

void main() {
  testWidgets('骨架不变:居中标题头+分隔线,取消为描边钮,确认为红底危险钮',
      (tester) async {
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: const Scaffold(
        body: ConfirmDialog(title: '删除会话', message: '不可恢复。'),
      ),
    ));

    // 头部骨架仍是 DialogHeader(居中标题+通栏分隔线)。
    expect(find.byType(DialogHeader), findsOneWidget);
    expect(find.byType(Divider), findsOneWidget);

    // 取消=描边钮;确认=FilledButton 且底色为主题 danger。
    expect(find.widgetWithText(OutlinedButton, '取消'), findsOneWidget);
    final confirm =
        find.widgetWithText(FilledButton, '删除');
    expect(confirm, findsOneWidget);
    final btn = tester.widget<FilledButton>(confirm);
    final t = buildAppTheme().extension<AppTokens>()!;
    expect(btn.style?.backgroundColor?.resolve({}), t.danger);
  });

  testWidgets('取消回传 false,确认回传 true', (tester) async {
    bool? result;
    await tester.pumpWidget(MaterialApp(
      theme: buildAppTheme(),
      home: Scaffold(
        body: Builder(
          builder: (ctx) => TextButton(
            onPressed: () async {
              result = await showDialog<bool>(
                context: ctx,
                builder: (_) => const ConfirmDialog(
                  title: '删除会话',
                  message: '不可恢复。',
                ),
              );
            },
            child: const Text('打开'),
          ),
        ),
      ),
    ));

    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(result, isFalse);

    await tester.tap(find.text('打开'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('删除'));
    await tester.pumpAndSettle();
    expect(result, isTrue);
  });
}
