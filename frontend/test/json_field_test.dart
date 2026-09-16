import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:msu_admin/ui/json_field.dart';

void main() {
  group('parseJsonObject', () {
    test('empty text is an empty object', () {
      final (value, error) = parseJsonObject('   ');
      expect(value, isEmpty);
      expect(error, isNull);
    });

    test('valid object parses', () {
      final (value, error) = parseJsonObject('{"temperature":0.6}');
      expect(value!['temperature'], 0.6);
      expect(error, isNull);
    });

    test('arrays and scalars are rejected', () {
      for (final raw in ['[1,2]', '"str"', '7', 'true']) {
        final (value, error) = parseJsonObject(raw);
        expect(value, isNull, reason: raw);
        expect(error, contains('JSON 对象'), reason: raw);
      }
    });

    test('malformed json reports a format error', () {
      final (value, error) = parseJsonObject('{oops');
      expect(value, isNull);
      expect(error, contains('JSON 格式错误'));
    });
  });

  test('prettyJson renders an empty map as {}', () {
    expect(prettyJson(const {}), '{}');
    expect(prettyJson(const {'a': 1}), contains('"a": 1'));
  });

  testWidgets('JsonField reports validity as the text changes', (tester) async {
    final controller = TextEditingController(text: '{}');
    final validity = <bool>[];

    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: JsonField(
          label: '默认参数',
          helper: 'helper',
          controller: controller,
          onValidityChanged: validity.add,
        ),
      ),
    ));

    controller.text = '{bad';
    await tester.pump();
    expect(validity.last, isFalse);
    expect(find.textContaining('JSON 格式错误'), findsOneWidget);

    controller.text = '{"temperature":0.6}';
    await tester.pump();
    expect(validity.last, isTrue);
    expect(find.textContaining('JSON 格式错误'), findsNothing);
  });
}
