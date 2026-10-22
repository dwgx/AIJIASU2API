set pagination off
break *0x418e36
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/48xb $rbx
x/48xb $rsi
info registers rdx rsi rbx
continue
