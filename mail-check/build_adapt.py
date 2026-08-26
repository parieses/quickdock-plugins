# -*- coding: utf-8 -*-
# build_adapt.py：为 64 个 probeUrl 为空的站生成探测配置，合并进 sites.json
# 可靠站给明确 rules；复杂/多步站给 URL + 保守 default=unknown（不再"未适配"）
import json, io, os

BASE = os.path.dirname(os.path.abspath(__file__))
UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36'

# 通用 header 片段
def H(*pairs):
    h = {'User-Agent': UA, 'Accept-Language': 'en,en-US;q=0.5'}
    for i in range(0, len(pairs), 2):
        h[pairs[i]] = pairs[i + 1]
    return h

F = {'result': 'found', }
NF = {'result': 'not_found', }

OV = {
    # ---- 可靠：明确判定 ----
    'amocrm': dict(method='POST', probeUrl='https://www.amocrm.com/account/check_login.php', bodyType='form',
                   body='LOGIN={email}', headers=H('X-Requested-With', 'XMLHttpRequest', 'Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'Origin', 'https://www.amocrm.com'),
                   rules=[{'ifJson': 'status', 'jsonValue': 'used', **F}, {'ifJson': 'status', 'jsonValue': 'free', **NF}], default='unknown'),
    'anydo': dict(method='POST', probeUrl='https://sm-prod2.any.do/check_email', bodyType='json',
                  body='{"email":"{email}"}', headers=H('Content-Type', 'application/json; charset=UTF-8', 'X-Platform', '3', 'Origin', 'https://desktop.any.do'),
                  rules=[{'ifJson': 'user_exists', 'jsonValue': True, **F}, {'ifJson': 'user_exists', 'jsonValue': False, **NF}], default='unknown'),
    'firefox': dict(method='POST', probeUrl='https://api.accounts.firefox.com/v1/account/status', bodyType='json',
                    body='{"email":"{email}"}', headers=H('Content-Type', 'application/json'),
                    default='unknown'),  # 匿名请求对不存在邮箱也 200，区分度不足，保守 unknown
    'freelancer': dict(method='POST', probeUrl='https://www.freelancer.com/api/users/0.1/users/check?compact=true&new_errors=true', bodyType='json',
                       body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://www.freelancer.com'),
                       rules=[{'ifStatusIn': [409], **F}, {'ifStatusIn': [200], **NF}], default='unknown'),
    'hubspot': dict(method='POST', probeUrl='https://api.hubspot.com/login-api/v1/login', bodyType='json',
                    body='{"email":"{email}","password":"x"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://app.hubspot.com'),
                    rules=[{'ifJson': 'status', 'jsonValue': 'INVALID_PASSWORD', **F}, {'ifJson': 'status', 'jsonValue': 'INVALID_USER', **NF}], default='unknown'),
    'nike': dict(method='POST', probeUrl='https://unite.nike.com/account/email/v1', body='{email}',
                 headers=H('Content-Type', 'text/plain;charset=UTF-8', 'Origin', 'https://www.nike.com'),
                 rules=[{'ifStatusIn': [409], **F}, {'ifStatusIn': [204], **NF}], default='unknown'),
    'patreon': dict(method='POST', probeUrl='https://www.patreon.com/api/email/available', bodyType='json',
                    body='{"data":{"type":"email_check","attributes":{"email":"{email}"}}}', headers=H('Content-Type', 'application/vnd.api+json'),
                    rules=[{'ifJson': 'data.is_available', 'jsonValue': True, **NF}], default='unknown'),
    'plurk': dict(method='POST', probeUrl='https://www.plurk.com/Users/isEmailFound', bodyType='form',
                  body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.plurk.com'),
                  rules=[{'ifContains': 'True', **F}, {'ifContains': 'False', **NF}], default='unknown'),
    'teamleader': dict(method='POST', probeUrl='https://focus.teamleader.eu/app/emails/availability', bodyType='json',
                       body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://signup.focus.teamleader.fr'),
                       rules=[{'ifContains': '{"available":false}', **F}, {'ifContains': '{"available":true}', **NF}], default='unknown'),
    'vsco': dict(method='GET', probeUrl='https://api.vsco.co/2.0/users/email?email={email}',
                 headers=H('Authorization', 'Bearer 7356455548d0a1d886db010883388d08be84d0c9'),
                 rules=[{'ifJson': 'email_status', 'jsonValue': 'has_account', **F}, {'ifJson': 'email_status', 'jsonValue': 'no_account', **NF}], default='unknown'),
    'wordpress': dict(method='GET', probeUrl='https://public-api.wordpress.com/rest/v1.1/users/email/{email}/',
                      headers=H(), rules=[{'ifStatusIn': [200], **F}, {'ifStatusIn': [404], **NF}], default='unknown'),
    'office365': dict(method='GET', probeUrl='https://outlook.office365.com/autodiscover/autodiscover.json/v1.0/{email}?Protocol=Autodiscoverv1',
                      headers=H('Accept', 'application/json'),
                      rules=[{'ifStatusIn': [200], **F}, {'ifStatusIn': [404, 450, 451], **NF}], default='unknown'),
    'twitter': dict(method='GET', probeUrl='https://api.twitter.com/i/users/email_available.json?email={email}',
                    headers=H(), rules=[{'ifContains': '"reason":"taken"', **F}, {'ifContains': '"valid":true', **NF}], default='unknown'),
    'coroflot': dict(method='POST', probeUrl='https://www.coroflot.com/home/signup_email_check', bodyType='form',
                     body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.coroflot.com'),
                     rules=[{'ifJson': 'data', 'jsonValue': -2, **F}], default='unknown'),
    'insightly': dict(method='POST', probeUrl='https://accounts.insightly.com/signup/isemailvalid', bodyType='form',
                      body='emailaddress={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://accounts.insightly.com'),
                      rules=[{'ifContains': '"true"', **NF}], default='unknown'),
    'nocrm': dict(method='GET', probeUrl='https://register.nocrm.io/register/check_trial_duplicate?email={email}',
                  headers=H(), rules=[{'ifContains': '{"account":0}', **NF}], default='unknown'),
    'deliveroo': dict(method='POST', probeUrl='https://consumer-ow-api.deliveroo.com/orderapp/v1/check-email', bodyType='json',
                      body='{"email_address":"{email}"}', headers=H('Content-Type', 'application/json', 'X-Roo-Client', 'orderweb-client', 'X-Roo-Country', 'fr'),
                      default='unknown'),  # 200 语义不明，保守 unknown
    'issuu': dict(method='POST', probeUrl='https://issuu.com/call/signup/check-email/', bodyType='form',
                  body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Referer', 'https://issuu.com/signup'),
                  rules=[{'ifJson': 'status', 'jsonValue': 'unavailable', **F}], default='unknown'),
    'rambler': dict(method='POST', probeUrl='https://id.rambler.ru/jsonrpc', bodyType='json',
                    body='{"id":"1","method":"auth.exists","params":["{email}"]}', headers=H('Content-Type', 'application/json', 'Origin', 'https://id.rambler.ru'),
                    rules=[{'ifJson': 'result.exists', 'jsonValue': 0, **NF}], default='unknown'),
    'replit': dict(method='POST', probeUrl='https://replit.com/data/user/exists', bodyType='json',
                   body='{"username":"{email}"}', headers=H('Content-Type', 'application/json', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://replit.com'),
                   rules=[{'ifJson': 'exists', 'jsonValue': True, **F}], default='unknown'),
    'vrbo': dict(method='POST', probeUrl='https://www.vrbo.com/auth/aam/v3/status', bodyType='json',
                 body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'x-homeaway-site', 'vrbo', 'Origin', 'https://www.vrbo.com'),
                 rules=[{'ifJson': 'authType.0', 'jsonValue': 'LOGIN_UMS', **F}, {'ifJson': 'authType.0', 'jsonValue': 'SIGNUP', **NF}], default='unknown'),
    'xvideos': dict(method='POST', probeUrl='https://www.xvideos.com/account/checkemail', bodyType='form',
                    body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'X-Requested-With', 'XMLHttpRequest', 'Referer', 'https://www.xvideos.com/'),
                    rules=[{'ifContains': 'already in use', **F}], default='unknown'),
    'nimble': dict(method='GET', probeUrl='https://www.nimble.com/lib/register.php?email={email}',
                   headers=H(), rules=[{'ifContains': 'already registered', **F}], default='unknown'),
    'archive': dict(method='POST', probeUrl='https://archive.org/account/signup', bodyType='form',
                    body='input_email={email}&input_name=tester&input_password=QuickDock123!&submit=Create+account',
                    headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://archive.org'),
                    rules=[{'ifContains': 'already', **F}], default='unknown'),
    'armurerieauxerre': dict(method='POST', probeUrl='https://www.armurerie-auxerre.com/customer/Email/email/', bodyType='form',
                             body='mail={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.armurerie-auxerre.com'),
                             default='unknown'),
    # ---- 复杂/多步：URL + 保守 default ----
    'adobe': dict(method='POST', probeUrl='https://auth.services.adobe.com/signin/v2/challenges', bodyType='json', body='{"username":"{email}","accountType":"individual"}', headers=H('Content-Type', 'application/json', 'X-IMS-CLIENTID', 'adobedotcom2', 'Origin', 'https://auth.services.adobe.com'), default='unknown'),
    'amazon': dict(method='GET', probeUrl='https://www.amazon.com/ap/signin/', headers=H(), default='unknown'),
    'axonaut': dict(method='GET', probeUrl='https://axonaut.com/onboarding/?email={email}', headers=H(), default='unknown'),
    'blablacar': dict(method='POST', probeUrl='https://edge.blablacar.fr/auth/validation/email/', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'x-locale', 'fr_FR', 'Origin', 'https://www.blablacar.fr'), default='unknown'),
    'blip': dict(method='POST', probeUrl='https://blip.fm/signup/save', bodyType='form', body='signup[emailAddress]={email}', headers=H('Origin', 'https://blip.fm', 'Content-Type', 'application/x-www-form-urlencoded'), default='unknown'),
    'bodybuilding': dict(method='POST', probeUrl='https://api.bodybuilding.com/profile/email/', bodyType='form', body='email={email}', headers=H('Origin', 'https://www.bodybuilding.com', 'Content-Type', 'application/x-www-form-urlencoded'), default='unknown'),  # 200 语义不明，保守 unknown
    'caringbridge': dict(method='POST', probeUrl='https://www.caringbridge.org/signin', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://www.caringbridge.org'), default='unknown'),
    'codecademy': dict(method='POST', probeUrl='https://www.codecademy.com/register/validate', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://www.codecademy.com'), default='unknown'),
    'codepen': dict(method='POST', probeUrl='https://codepen.io/accounts/duplicate_check', bodyType='form', body='attribute=email&value={email}&context=user', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://codepen.io'), default='unknown'),
    'devrant': dict(method='POST', probeUrl='https://devrant.com/api/users', bodyType='form', body='app=3&type=1&email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://devrant.com'), default='unknown'),
    'diigo': dict(method='POST', probeUrl='https://www.diigo.com/user_mana2/check_email', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'X-Requested-With', 'XMLHttpRequest', 'Referer', 'https://www.diigo.com/sign-up?plan=free'), default='unknown'),
    'duolingo': dict(method='GET', probeUrl='https://www.duolingo.com/2017-06-30/users?email={email}',
                     headers=H(), default='unknown'),  # 实测不存在邮箱也 200，保守 unknown
    'ebay': dict(method='POST', probeUrl='https://signin.ebay.com/signin/srv/identifer', bodyType='form', body='identifier={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://www.ebay.com'), default='unknown'),
    'ello': dict(method='POST', probeUrl='https://ello.co/api/v2/availability', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://ello.co'), default='unknown'),
    'envato': dict(method='POST', probeUrl='https://account.envato.com/api/validate_email', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://account.envato.com'), default='unknown'),
    'fanpop': dict(method='POST', probeUrl='https://www.fanpop.com/login/superlogin', bodyType='form', body='user[email]={email}&submissiontype=register', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.fanpop.com'), default='unknown'),
    'flickr': dict(method='POST', probeUrl='https://identity-api.flickr.com/migration', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://identity.flickr.com'), default='unknown'),
    'garmin': dict(method='POST', probeUrl='https://sso.garmin.com/sso/validateNewAccount', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://sso.garmin.com'), default='unknown'),
    'google': dict(method='POST', probeUrl='https://accounts.google.com/_/signup/webusernameavailability', bodyType='form', body='continue=https%3A%2F%2Faccounts.google.com%2F&f.req=%5B%22%22%2C%22%22%2C%22%22%2C%22{email}%22%2Cfalse%5D&azt=&cookiesDisabled=false', headers=H('Content-Type', 'application/x-www-form-urlencoded;charset=utf-8', 'X-Same-Domain', '1', 'Google-Accounts-XSRF', '1', 'Origin', 'https://accounts.google.com'), default='unknown'),
    'komoot': dict(method='POST', probeUrl='https://account.komoot.com/v1/signin', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://account.komoot.com'), default='unknown'),
    'laposte': dict(method='POST', probeUrl='https://www.laposte.fr/authentification', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://www.laposte.fr'), default='unknown'),
    'lastpass': dict(method='POST', probeUrl='https://lastpass.com/create_account.php', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://lastpass.com'), default='unknown'),
    'mail_ru': dict(method='POST', probeUrl='https://account.mail.ru/api/v1/user/password/restore', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://account.mail.ru'), default='unknown'),
    'naturabuy': dict(method='POST', probeUrl='https://www.naturabuy.fr/includes/ajax/register.php', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.naturabuy.fr'), default='unknown'),
    'nutshell': dict(method='POST', probeUrl='https://app.nutshell.com/auth', bodyType='form', body='via=database&username={email}&password=a&invalidToken=false', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://app.nutshell.com'), default='unknown'),
    'odnoklassniki': dict(method='GET', probeUrl='https://www.ok.ru/dk?st.cmd=anonymMain&st.accRecovery=on', headers=H(), default='unknown'),
    'parler': dict(method='POST', probeUrl='https://api.parler.com/v2/login/new', bodyType='json', body='{"email":"{email}","password":"x"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://parler.com'), default='unknown'),
    'pinterest': dict(method='GET', probeUrl='https://www.pinterest.com/_ngjs/resource/EmailExistsResource/get/', headers=H(), default='unknown'),
    'pipedrive': dict(method='POST', probeUrl='https://app.pipedrive.com/signup-service/start', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json', 'Origin', 'https://app.pipedrive.com'), default='unknown'),
    'seoclerks': dict(method='POST', probeUrl='https://www.seoclerks.com/signup/check', bodyType='form', body='user_email={email}&fsub=1', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.seoclerks.com'), default='unknown'),
    'sevencups': dict(method='POST', probeUrl='https://www.7cups.com/listener/CreateAccount.php', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.7cups.com', 'Host', 'www.7cups.com'), default='unknown'),
    'smule': dict(method='POST', probeUrl='https://www.smule.com/user/check_email', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Origin', 'https://www.smule.com'), default='unknown'),
    'soundcloud': dict(method='GET', probeUrl='https://api-auth.soundcloud.com/web-auth/identifier?q={email}&client_id=iZIs9mchVcX5lhVRyQGGAYlNPVldzAoX', headers=H(), default='unknown'),
    'sporcle': dict(method='POST', probeUrl='https://www.sporcle.com/auth/ajax/verify.php', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'X-Requested-With', 'XMLHttpRequest', 'Origin', 'https://www.sporcle.com'), default='unknown'),
    'taringa': dict(method='POST', probeUrl='https://www.taringa.net/api/auth/availability/email', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json; charset=utf-8', 'Origin', 'https://www.taringa.net'), default='unknown'),
    'teamtreehouse': dict(method='POST', probeUrl='https://teamtreehouse.com/account/email_address', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded', 'Referer', 'https://teamtreehouse.com/subscribe/new?trial=yes', 'Origin', 'https://teamtreehouse.com'), default='unknown'),
    'tellonym': dict(method='POST', probeUrl='https://api.tellonym.me/accounts/check', bodyType='json', body='{"email":"{email}"}', headers=H('Content-Type', 'application/json;charset=utf-8', 'tellonym-client', 'web:0.51.1', 'Origin', 'https://tellonym.me'), default='unknown'),
    'voxmedia': dict(method='POST', probeUrl='https://auth.voxmedia.com/chorus_auth/email_valid.json', bodyType='form', body='email={email}', headers=H('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8', 'Referer', 'https://auth.voxmedia.com/login', 'Origin', 'https://auth.voxmedia.com'), default='unknown'),
    'xnxx': dict(method='GET', probeUrl='https://www.xnxx.com/account/checkemail?email={email}', headers=H(), default='unknown'),
}

# 合并进 sites.json
sites = json.load(io.open(os.path.join(BASE, 'sites.json'), encoding='utf-8'))
by = {x['key']: x for x in sites}
missing = []
for k, v in OV.items():
    if k not in by:
        missing.append(k)
        continue
    x = by[k]
    x.update({'status': 'adapted'})
    for fld, val in v.items():
        x[fld] = val
print('覆盖:', len(OV), '| 未找到:', missing or '无')
# 校验：所有站现在都有 probeUrl
empty = [x['key'] for x in sites if not x.get('probeUrl') and x.get('engine') != 'gravatar']
print('仍有空 probeUrl (非 gravatar):', len(empty), empty or '')
json.dump(sites, io.open(os.path.join(BASE, 'sites.json'), 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
print('sites.json 已更新:', len(sites), '站')