import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:msu_admin/api_client.dart';

ApiClient clientReturning(
  int status,
  String body, {
  void Function(http.Request)? inspect,
}) {
  final mock = MockClient((request) async {
    inspect?.call(request);
    return http.Response(body, status,
        headers: {'content-type': 'application/json'});
  });
  return ApiClient(
      baseUrl: 'http://127.0.0.1:8080', adminKey: 'adm', httpClient: mock);
}

void main() {
  test('unauthorized maps to UnauthorizedException', () async {
    final client = clientReturning(401, '{"error":{"code":"unauthorized"}}');
    expect(client.listAccounts(), throwsA(isA<UnauthorizedException>()));
  });

  test('validation error carries the server message and code', () async {
    final client = clientReturning(400, jsonEncode({
      'error': {
        'code': 'invalid_protocol',
        'message': 'provider "kimi" does not support protocol "responses"',
        'status': 400,
      }
    }));
    try {
      await client.listAccounts();
      fail('expected ValidationException');
    } on ValidationException catch (e) {
      expect(e.code, 'invalid_protocol');
      expect(e.message, contains('does not support protocol'));
      expect(e.status, 400);
    }
  });

  test('unreachable service maps to UnreachableException with the address',
      () async {
    final mock = MockClient((_) async => throw const HttpExceptionStub());
    final client = ApiClient(
        baseUrl: 'http://127.0.0.1:9', adminKey: 'adm', httpClient: mock);
    try {
      await client.listAccounts();
      fail('expected UnreachableException');
    } on UnreachableException catch (e) {
      expect(e.baseUrl, 'http://127.0.0.1:9');
      expect(e.message, contains('http://127.0.0.1:9'));
    }
  });

  test('non-json error body still yields a validation error', () async {
    final client = clientReturning(500, '<html>oops</html>');
    expect(client.listAccounts(), throwsA(isA<ValidationException>()));
  });

  test('admin key is sent as a bearer token', () async {
    String? auth;
    final client = clientReturning(200, '{"accounts":[]}',
        inspect: (r) => auth = r.headers['Authorization']);
    await client.listAccounts();
    expect(auth, 'Bearer adm');
  });

  test('trailing slash in base url does not double up', () async {
    Uri? seen;
    final mock = MockClient((request) async {
      seen = request.url;
      return http.Response('{"accounts":[]}', 200);
    });
    final client = ApiClient(
        baseUrl: 'http://127.0.0.1:8080/', adminKey: 'adm', httpClient: mock);
    await client.listAccounts();
    expect(seen!.path, '/admin/accounts');
  });

  test('204 responses decode as empty', () async {
    final client = clientReturning(204, '');
    await client.deleteModel('kimi-1/k2');
  });

  test('account parsing keeps the masked key only', () async {
    final client = clientReturning(200, jsonEncode({
      'accounts': [
        {
          'name': 'kimi-1',
          'provider_id': 'kimi',
          'credential': {'kind': 'api_key', 'api_key': 'sk-l***efgh'},
          'base_url': '',
          'headers': {'x-trace': 'on'},
          'enabled': true,
        }
      ]
    }));
    final accounts = await client.listAccounts();
    expect(accounts.single.maskedApiKey, 'sk-l***efgh');
    expect(accounts.single.headers['x-trace'], 'on');
    expect(accounts.single.enabled, isTrue);
  });

  test('provider without a quota block is not queryable', () async {
    final client = clientReturning(200, jsonEncode({
      'providers': [
        {
          'id': 'anthropic',
          'display_name': 'Anthropic',
          'website': 'https://www.anthropic.com',
          'base_url': 'https://api.anthropic.com',
          'protocols': ['anthropic'],
          'auth': 'anthropic_key',
          'credential': 'api_key',
        },
        {
          'id': 'deepseek',
          'display_name': 'DeepSeek',
          'website': 'https://platform.deepseek.com',
          'base_url': 'https://api.deepseek.com',
          'protocols': ['anthropic', 'chat_completions'],
          'auth': 'bearer',
          'credential': 'api_key',
          'quota': {'path': '/user/balance', 'method': 'GET', 'reset': 'prepaid'},
        },
      ]
    }));
    final providers = await client.listProviders();
    expect(providers.first.quotaQueryable, isFalse);
    expect(providers.last.quotaQueryable, isTrue);
    expect(providers.last.quotaReset, 'prepaid');
  });

  test('update omits the api key when it is blank', () async {
    Map<String, dynamic>? sent;
    final mock = MockClient((request) async {
      sent = jsonDecode(request.body) as Map<String, dynamic>;
      return http.Response(
          jsonEncode({'account': {'name': 'kimi-1', 'enabled': false}}), 200);
    });
    final client = ApiClient(
        baseUrl: 'http://127.0.0.1:8080', adminKey: 'adm', httpClient: mock);
    await client.updateAccount(name: 'kimi-1', apiKey: '', enabled: false);
    expect(sent!.containsKey('api_key'), isFalse,
        reason: 'blank key must not overwrite the stored credential');
    expect(sent!['enabled'], isFalse);
  });

  test('quota report parses the not-queryable shape', () async {
    final client = clientReturning(
        200, jsonEncode({'account': 'kimi-1', 'queryable': false}));
    final report = await client.queryQuota('kimi-1');
    expect(report.queryable, isFalse);
    expect(report.remaining, isNull);
  });
}

/// HttpExceptionStub 模拟传输层失败。
class HttpExceptionStub implements Exception {
  const HttpExceptionStub();
}
