set pagination off
break *0x45183c
commands
silent
x/8xb $rbp-0xe4
continue
end
break *0x451937
commands
silent
x/40xb *(void**)($rbp-0x30)
continue
end
run -c /tmp/ajs-lab/ajs.conf connect Test*
