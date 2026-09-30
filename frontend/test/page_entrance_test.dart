import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:msu_admin/ui/page_entrance.dart';

double _opacity(WidgetTester tester) => tester
    .widget<FadeTransition>(find.descendant(
        of: find.byType(PageEntrance), matching: find.byType(FadeTransition)))
    .opacity
    .value;

Widget _wrap(Object? trigger) => MaterialApp(
      home: PageEntrance(trigger: trigger, child: const Text('内容')),
    );

void main() {
  testWidgets('挂载时以 0.5s 从透明淡入到不透明', (tester) async {
    await tester.pumpWidget(_wrap(null));
    // 首帧动画刚开始:接近全透明。
    expect(_opacity(tester), lessThan(0.05));

    // 半程:半透明。
    await tester.pump(const Duration(milliseconds: 250));
    final mid = _opacity(tester);
    expect(mid, greaterThan(0.2));
    expect(mid, lessThan(0.9));

    // 结束:完全不透明,子树内容一直在(动画不重建子树)。
    await tester.pumpAndSettle();
    expect(_opacity(tester), 1.0);
    expect(find.text('内容'), findsOneWidget);
  });

  testWidgets('trigger 变化重播淡入,不变则不重播', (tester) async {
    await tester.pumpWidget(_wrap('a'));
    await tester.pumpAndSettle();
    expect(_opacity(tester), 1.0);

    // trigger 不变:保持不透明。
    await tester.pumpWidget(_wrap('a'));
    await tester.pump();
    expect(_opacity(tester), 1.0);

    // trigger 变化:从头重播。
    await tester.pumpWidget(_wrap('b'));
    await tester.pump();
    expect(_opacity(tester), lessThan(0.05));
    await tester.pumpAndSettle();
    expect(_opacity(tester), 1.0);
  });
}
