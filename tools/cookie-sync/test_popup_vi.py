"""Exercise translated popup in an isolated browser using synthetic data only."""
import pathlib
import tempfile
from playwright.sync_api import sync_playwright

root = pathlib.Path(__file__).resolve().parent
popup = root / 'ext-src' / 'popup.html'
artifacts = root / 'validation'
artifacts.mkdir(exist_ok=True)
with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    page = browser.new_page(viewport={'width':440,'height':1500})
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.add_init_script('''
      window.fixture = {config: {controller:'http://localhost:8083', pushMode:'service',
        profile:'laptop', token:'fixture-key', enabled:true, autoKeepalive:true,
        boundEmail:'a@example.test'}, saves:0, actions:[], bindings:[
          {boundEmail:'a@example.test',maintenanceState:'ok'},
          {boundEmail:'b@example.test',maintenanceState:'needs_attention',
           lastSyncDetail:'<img src=x onerror=alert(1)>'}]};
      window.chrome = {
        runtime: { sendMessage(message, done) {
          fixture.actions.push(message.type);
          if(message.type==='selectBinding') fixture.config.boundEmail=message.email;
          if (message.type === 'setConfig') { Object.assign(fixture.config,message.patch); fixture.saves++; }
          done({ok:true,detail:'fixture success',bindings:fixture.bindings,config:fixture.config,logged_in:false,cookie_count:0,
            bound:{tab_id:0,detail:'Chưa liên kết tab Gemini'}});
        }},
        tabs: {query(options,done) {done([{id:7,url:'https://gemini.google.com/app',windowId:1}]);}}
      };
    ''')
    page.goto(popup.as_uri())
    page.locator('#controller').wait_for()
    assert page.locator('#bindingCount').inner_text() == '2'
    assert page.locator('#maintenanceOwner').inner_text() == 'VPS tự duy trì'
    assert page.locator('#btnKa').is_disabled()
    assert page.locator('#autoKeepalive').is_disabled()
    assert page.locator('#bindingRows img').count() == 0
    page.locator('#bindingSelect').select_option('b@example.test')
    assert page.evaluate('fixture.config.boundEmail') == 'b@example.test'
    page.locator('#btnSyncAll').click()
    page.locator('#btnBindOpen').click()
    assert page.evaluate("fixture.actions.includes('syncAll') && fixture.actions.includes('bindOpenTabs')")
    assert page.locator('html').get_attribute('lang') == 'vi'
    assert page.locator('#token').get_attribute('type') == 'password'
    assert page.locator('#login').inner_text() == 'Chưa đăng nhập / thiếu cookie phiên'
    page.locator('#profile').fill('ho-so-thu-nghiem')
    # Periodic status refresh must not overwrite an in-progress edit.
    page.evaluate('render()')
    assert page.locator('#profile').input_value() == 'ho-so-thu-nghiem'
    page.locator('#btnSave').click()
    assert page.evaluate('fixture.config.profile') == 'ho-so-thu-nghiem'
    assert page.evaluate('fixture.saves') == 1
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
    for item in page.locator('button').all():
        assert item.is_visible()
    assert not errors, errors
    page.screenshot(path=str(artifacts/'popup-vi.png'),full_page=True)
    browser.close()
print('PASS: Vietnamese popup renders, masks key, saves settings and preserves edits; no browser errors.')
