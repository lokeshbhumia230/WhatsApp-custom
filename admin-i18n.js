(function(){
const L={
 en:{name:'English'},hi:{name:'हिन्दी'},pt:{name:'Português'},es:{name:'Español'},id:{name:'Bahasa Indonesia'},bn:{name:'বাংলা'},ur:{name:'اردو'},ar:{name:'العربية'}
};
const T={
'WhatsApp SaaS Control Center':{hi:'WhatsApp SaaS नियंत्रण केंद्र',pt:'Central de Controle SaaS do WhatsApp',es:'Centro de Control SaaS de WhatsApp',id:'Pusat Kontrol SaaS WhatsApp',bn:'WhatsApp SaaS কন্ট্রোল সেন্টার',ur:'WhatsApp SaaS کنٹرول سینٹر',ar:'مركز تحكم WhatsApp SaaS'},
'Dashboard':{hi:'डैशबोर्ड',pt:'Painel',es:'Panel',id:'Dasbor',bn:'ড্যাশবোর্ড',ur:'ڈیش بورڈ',ar:'لوحة التحكم'},
'Devices':{hi:'डिवाइस',pt:'Dispositivos',es:'Dispositivos',id:'Perangkat',bn:'ডিভাইস',ur:'ڈیوائسز',ar:'الأجهزة'},
'Pair WhatsApp':{hi:'WhatsApp जोड़ें',pt:'Vincular WhatsApp',es:'Vincular WhatsApp',id:'Hubungkan WhatsApp',bn:'WhatsApp পেয়ার করুন',ur:'WhatsApp جوڑیں',ar:'ربط WhatsApp'},
'Settings':{hi:'सेटिंग्स',pt:'Configurações',es:'Configuración',id:'Pengaturan',bn:'সেটিংস',ur:'ترتیبات',ar:'الإعدادات'},
'Logout':{hi:'लॉगआउट',pt:'Sair',es:'Cerrar sesión',id:'Keluar',bn:'লগআউট',ur:'لاگ آؤٹ',ar:'تسجيل الخروج'},
'Connected':{hi:'कनेक्टेड',pt:'Conectado',es:'Conectado',id:'Terhubung',bn:'সংযুক্ত',ur:'منسلک',ar:'متصل'},
'Offline':{hi:'ऑफलाइन',pt:'Offline',es:'Desconectado',id:'Offline',bn:'অফলাইন',ur:'آف لائن',ar:'غير متصل'},
'Total devices':{hi:'कुल डिवाइस',pt:'Total de dispositivos',es:'Dispositivos totales',id:'Total perangkat',bn:'মোট ডিভাইস',ur:'کل ڈیوائسز',ar:'إجمالي الأجهزة'},
'WhatsApp Devices':{hi:'WhatsApp डिवाइस',pt:'Dispositivos WhatsApp',es:'Dispositivos de WhatsApp',id:'Perangkat WhatsApp',bn:'WhatsApp ডিভাইস',ur:'WhatsApp ڈیوائسز',ar:'أجهزة WhatsApp'},
'Open Devices':{hi:'डिवाइस खोलें',pt:'Abrir dispositivos',es:'Abrir dispositivos',id:'Buka perangkat',bn:'ডিভাইস খুলুন',ur:'ڈیوائسز کھولیں',ar:'فتح الأجهزة'},
'Refresh':{hi:'रिफ्रेश',pt:'Atualizar',es:'Actualizar',id:'Segarkan',bn:'রিফ্রেশ',ur:'ریفریش',ar:'تحديث'},
'Sync':{hi:'सिंक',pt:'Sincronizar',es:'Sincronizar',id:'Sinkronkan',bn:'সিঙ্ক',ur:'سنک',ar:'مزامنة'},
'Reconnect':{hi:'फिर से कनेक्ट करें',pt:'Reconectar',es:'Reconectar',id:'Sambungkan kembali',bn:'পুনরায় সংযোগ',ur:'دوبارہ منسلک کریں',ar:'إعادة الاتصال'},
'View Details':{hi:'विवरण देखें',pt:'Ver detalhes',es:'Ver detalles',id:'Lihat detail',bn:'বিস্তারিত দেখুন',ur:'تفصیلات دیکھیں',ar:'عرض التفاصيل'},
'Send Test Message':{hi:'टेस्ट संदेश भेजें',pt:'Enviar mensagem de teste',es:'Enviar mensaje de prueba',id:'Kirim pesan uji',bn:'টেস্ট মেসেজ পাঠান',ur:'ٹیسٹ پیغام بھیجیں',ar:'إرسال رسالة اختبار'},
'Logout Device':{hi:'डिवाइस लॉगआउट',pt:'Sair do dispositivo',es:'Cerrar dispositivo',id:'Keluarkan perangkat',bn:'ডিভাইস লগআউট',ur:'ڈیوائس لاگ آؤٹ',ar:'تسجيل خروج الجهاز'},
'Last updated':{hi:'अंतिम अपडेट',pt:'Última atualização',es:'Última atualização',id:'Terakhir diperbarui',bn:'সর্বশেষ আপডেট',ur:'آخری اپডেট',ar:'آخر تحديث'},
'Pair New WhatsApp':{hi:'नया WhatsApp जोड़ें',pt:'Vincular novo WhatsApp',es:'Vincular nuevo WhatsApp',id:'Hubungkan WhatsApp baru',bn:'নতুন WhatsApp পেয়ার করুন',ur:'نیا WhatsApp جوڑیں',ar:'ربط WhatsApp جديد'},
'Generate Pairing Code':{hi:'पेयरिंग कोड बनाएं',pt:'Gerar código de pareamento',es:'Generar código de vinculación',id:'Buat kode pemasangan',bn:'পেয়ারিং কোড তৈরি করুন',ur:'پیئرنگ کوڈ بنائیں',ar:'إنشاء رمز الربط'},
'App User ID':{hi:'ऐप यूज़र ID',pt:'ID do usuário',es:'ID de usuario',id:'ID pengguna aplikasi',bn:'অ্যাপ ইউজার ID',ur:'ایپ یوزر ID',ar:'معرف مستخدم التطبيق'},
'WhatsApp phone number':{hi:'WhatsApp फोन नंबर',pt:'Número de telefone do WhatsApp',es:'Número de teléfono de WhatsApp',id:'Nomor telepon WhatsApp',bn:'WhatsApp ফোন নম্বর',ur:'WhatsApp فون نمبر',ar:'رقم هاتف WhatsApp'},
'Language':{hi:'भाषा',pt:'Idioma',es:'Idioma',id:'Bahasa',bn:'ভাষা',ur:'زبان',ar:'اللغة'}
};
function translate(){const lang=localStorage.getItem('88task_lang')||'en';document.documentElement.lang=lang;document.querySelectorAll('title').forEach(x=>{const raw=x.textContent; if(T[raw])x.textContent=T[raw][lang]||raw});const walker=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);let n;while(n=walker.nextNode()){if(n.parentElement&&['SCRIPT','STYLE','OPTION'].includes(n.parentElement.tagName))continue;const raw=n.nodeValue.trim();if(!raw)continue;for(const k in T){if(raw===k){n.nodeValue=n.nodeValue.replace(raw,T[k][lang]||raw);break}}}document.querySelectorAll('input[placeholder]').forEach(x=>{const k=x.placeholder;if(T[k])x.placeholder=T[k][lang]||k});document.querySelectorAll('[data-i18n]').forEach(x=>{const k=x.dataset.i18n;if(T[k])x.textContent=T[k][lang]||k});const s=document.getElementById('lang-select');if(s)s.value=lang}
function addSelector(){if(document.getElementById('lang-select'))return;const box=document.createElement('div');box.style='position:fixed;top:14px;right:14px;z-index:9999';box.innerHTML='<select id="lang-select" aria-label="Language" style="padding:8px 30px 8px 10px;border:1px solid #ddd;border-radius:9px;background:#fff;color:#111;font-weight:650;box-shadow:0 5px 18px #0001"></select>';document.body.appendChild(box);const s=box.firstElementChild;Object.entries(L).forEach(([k,v])=>{const o=document.createElement('option');o.value=k;o.textContent=v.name;s.appendChild(o)});s.onchange=()=>{localStorage.setItem('88task_lang',s.value);location.reload()};s.value=localStorage.getItem('88task_lang')||'en'}
addEventListener('DOMContentLoaded',()=>{addSelector();translate()});
})();